/* Offline design model. No database access; one synchronous action per event. */
(function(global){
'use strict';
const scenarios={
 mixed:{label:'50 条 × 5 shard：混合差异',desc:'每片 10 条老表数据；包含相等、缺失、不同，并给每片增加 1 条仅新表记录。'},
 equal:{label:'全部相等',desc:'所有记录在内存初比后完成，不开启修复事务。'},
 missing:{label:'新表全部缺失',desc:'50 条都进入插入批，按 shard 和写入上限拆分。'},
 different:{label:'全部不同',desc:'50 条都需要更新；批量复核后在同一事务中逐条 UPDATE，共 50 次更新。'},
 skew:{label:'分片倾斜',desc:'40 条路由到 shard 1，其余 10 条落到另外四片；仍是有界顺序处理。'},
 sparse:{label:'100 shard，50 条稀疏分布',desc:'前 50 片各 1 条；其余 50 片为空，也必须扫描成功结束。'},
 stale:{label:'初比不同，复核前已同步',desc:'候选 4 在事务开始前被外部同步为相等，重读后跳过，不覆盖。'},
 lookupFail:{label:'完整查询失败',desc:'首个配对批查询失败，停止；不能把无结果当缺失，安全游标不推进。'},
 rollback:{label:'批量写入失败并回滚',desc:'首个修复事务暂存写入后失败，回滚；没有修复计数，也不释放失败条目。'},
 uncertain:{label:'提交响应丢失',desc:'数据库实际已提交，但客户端不知道；先标记结果未知，再重读确认，避免盲目重复写入。'},
 cancel:{label:'攒批后取消',desc:'进入事务前取消，保留未处理状态；读取游标 50，安全游标仍为起点。'},
 alert:{label:'只报警模式',desc:'二次复核后报警，无 INSERT / UPDATE；额外记录始终保留。'},
 tail:{label:'50 条，扫描页 17：尾批',desc:'17 + 17 + 16 条，尾批不足阈值也处理，最后空页确认 EOF。'}
};
function build(scene='mixed'){
 if(!scenarios[scene])throw Error('unknown scenario');
 const count=50, shards=scene==='sparse'?100:5, scanSize=scene==='tail'?17:50, reviewSize=8, insertSize=4;
 const route=id=>scene==='skew'?(id<=40?1:2+(id-41)%4):1+(id-1)%shards;
 const old=new Map(), fresh=new Map();
 for(let id=1;id<=count;id++){old.set(id,'old-'+id);if(scene!=='missing' && (scene==='equal'||scene==='different'||id%7!==0))fresh.set(id,scene==='equal'?'old-'+id:(scene==='different'||id%4===0)?'stale-'+id:'old-'+id);}
 const extras=['mixed','skew','stale','alert','tail'].includes(scene)?Array.from({length:shards},(_,i)=>101+i):[];
 extras.forEach(id=>fresh.set(id,'extra-'+id));
 const before=Object.fromEntries(fresh), events=[];
 const state={scene,shards,scanSize,reviewSize,insertSize,old:{phase:'未开始',read:0,safe:0,rows:0,batches:0},tables:Array.from({length:shards},(_,i)=>({shard:i+1,phase:'未开始',cursor:0,rows:0})),memory:[],results:{equal:0,skipped:0,repaired:0,alerted:0},io:{scan:0,lookup:0,recheck:0,insert:0,update:0,begin:0,commit:0,rollback:0},tx:null,batch:'—',status:'RUNNING',peak:0};
 const copy=x=>JSON.parse(JSON.stringify(x));
 function emit(node,title,detail,db=null){state.peak=Math.max(state.peak,state.memory.length);if(state.memory.length>50)throw Error('capacity exceeded');if(db)state.io[db]++;events.push({node,title,detail,db,state:copy(state)});}
 function find(id){return state.memory.find(x=>x.id===id);}
 function set(ids,phase){ids.forEach(id=>{const r=find(id);if(r)r.phase=phase;});}
 function clear(ids){state.memory=state.memory.filter(x=>!ids.includes(x.id));}
 function rows(ids,side){ids.forEach(id=>{if(!find(id))state.memory.push({id,shard:route(id),old:side==='old'?'PRESENT':'UNKNOWN',new:side==='new'?'PRESENT':'UNKNOWN',oldValue:side==='old'?old.get(id):null,newValue:side==='new'?fresh.get(id):null,oldScan:side==='old',newScan:side==='new',phase:'等待配对',kind:null});});}
 function classify(id){const a=old.has(id),b=fresh.has(id);return !a&&b?'extra':a&&!b?'missing':a&&b&&old.get(id)!==fresh.get(id)?'different':'equal';}
 let failed=false,txNo=0,staleDone=false,unknownDone=false;
 function reconcile(ids,reverse){
  const groups=new Map();ids.forEach(id=>{const sh=route(id);if(!groups.has(sh))groups.set(sh,[]);groups.get(sh).push(id);});
  for(const [sh,keys] of groups){
   state.batch='配对 · shard '+sh+' · '+keys.length+' keys';
   if(scene==='lookupFail'){set(keys,'失败');state.status='FAILED';emit('failure','配对查询失败','未获得完整结果：不产生 ABSENT，不执行初比、不开启事务。安全游标不变。','lookup');failed=true;return;}
   keys.forEach(id=>{const r=find(id);if(reverse){r.old=old.has(id)?'PRESENT':'ABSENT';r.oldValue=old.get(id)??null;}else{r.new=fresh.has(id)?'PRESENT':'ABSENT';r.newValue=fresh.get(id)??null;}r.phase='等待初比';});
   emit('lookup','完整批量配对查询','一次查询 shard '+sh+' 的 '+keys.length+' 个 key；'+(reverse?'查询老表':'查询新分片')+'。Scan 游标不会因为 Lookup 前进。','lookup');
   const candidates=[],equal=[];
   keys.forEach(id=>{const r=find(id),k=classify(id);r.kind=k;if(k==='equal'){r.phase='初比相等';equal.push(id);state.results.equal++;}else{r.phase='等待复核攒批';candidates.push(id);}});
   emit('compare','内存初比 / 候选攒批',equal.length+' 条相等，'+candidates.length+' 条候选。这里不访问 DB。');
   if(equal.length){clear(equal);emit('release','释放相等记录','终态结果已记录，释放 '+equal.length+' 条，Memory 计数随之减少。');}
   for(let start=0;start<candidates.length;start+=reviewSize){
    const batch=candidates.slice(start,start+reviewSize);state.batch='复核 R'+(++txNo)+' · shard '+sh+' · '+batch.length+' keys';set(batch,'复核批就绪');
    emit('queue','复核批就绪',batch.length+' 条；上限 '+reviewSize+' 条。当前扫描页已处理到尾，未满复核批也直接发送，不等未来页面来凑。');
    if(scene==='cancel'){state.status='CANCELLED';set(batch,'取消');emit('cancel','事务前取消','没有开启事务。其余未处理记录仍在 Memory；不推进安全检查点。');failed=true;return;}
    if(scene==='stale'&&!staleDone&&batch.includes(4)){fresh.set(4,old.get(4));staleDone=true;emit('external','外部数据在复核前改变','模拟业务已同步 key 4；这是外部变化，不是本单线程另开任务。');}
    state.tx={shard:sh,phase:'ACTIVE',staged:[],rows:batch.slice()};set(batch,'事务复核中');emit('begin','BEGIN：单个业务事务','攒批已结束才开启事务。同一实例示例，锁顺序先老表、后目标新分片。','begin');
    emit('readOld','批量重读老表','同一事务内一次读取 '+batch.length+' 个 key；缓存快照不能作为写入依据。','recheck');
    batch.forEach(id=>{find(id).old=old.has(id)?'PRESENT':'ABSENT';find(id).new=fresh.has(id)?'PRESENT':'ABSENT';find(id).oldValue=old.get(id)??null;find(id).newValue=fresh.get(id)??null;});
    emit('readNew','批量重读新分片','继续在同一个事务内，读取 shard '+sh+' 的 '+batch.length+' 个 key。一次只执行一个 DB 动作。','recheck');
    const insert=[],update=[],alerts=[],skip=[];
    batch.forEach(id=>{const k=classify(id),r=find(id);r.kind=k;if(k==='equal'){r.phase='相等跳过';skip.push(id);}else if(k==='extra'||scene==='alert'){r.phase='等待报警';alerts.push(id);}else if(k==='missing'){r.phase='待批量插入';insert.push(id);}else{r.phase='待逐条更新';update.push(id);}});
    emit('classify','事务内重新分类',insert.length+' 待插入、'+update.length+' 待更新、'+alerts.length+' 待报警、'+skip.length+' 跳过。分类后立即继续，不跨事务排队。');
    for(let i=0;i<insert.length;i+=insertSize){const chunk=insert.slice(i,i+insertSize);set(chunk,'写入中');state.tx.staged.push(...chunk);emit('insert','批量 INSERT',chunk.length+' 条，同 shard 插入；本演示每条 INSERT 上限 '+insertSize+' 条。事务尚未提交。','insert');}
    for(const id of update){set([id],'写入中');state.tx.staged.push(id);emit('update','逐条 UPDATE：key '+id,'本次仅更新 1 条记录；同一事务内顺序处理下一个 key，不支持批量 UPDATE。尚未提交，不计成功。','update');}
    if(scene==='rollback'&&(insert.length||update.length)){state.tx.phase='ROLLING_BACK';emit('rollback','写入失败 → ROLLBACK','暂存操作全部丢弃。事务中此前执行的语句也不视为成功。','rollback');state.tx=null;set(batch,'失败');state.status='FAILED';emit('failure','本轮停止','失败批保留；此前已提交批次不会回滚。安全游标不越过未完成页面。');failed=true;return;}
    set(batch,'等待提交');state.tx.phase='COMMITTING';emit('commit','COMMIT：等待确认','SQL 已执行不等于成功。确认提交前，不增加修复/告警成功数，不释放本批。','commit');
    const changed=[...insert,...update];changed.forEach(id=>fresh.set(id,old.get(id)));
    if(scene==='uncertain'&&!unknownDone&&changed.length){unknownDone=true;state.tx=null;set(batch,'提交结果未知');state.status='UNKNOWN';emit('uncertain','提交响应丢失','演示设定：服务端已提交，但客户端无法确认；安全游标和成功计数保持不变。');
     emit('confirm','重新批量读取老表','模拟恢复处理的第一步：先重读老表。这里只验证本场景的固定数据，不是通用跨实例恢复协议。','recheck');batch.forEach(id=>{find(id).new=fresh.has(id)?'PRESENT':'ABSENT';find(id).newValue=fresh.get(id)??null;});emit('confirm','重新批量读取新分片','确认本批最新内容已与老表一致，再幂等记为已确认完成；没有盲目重复写入。','recheck');state.status='RUNNING';}
    changed.forEach(id=>{find(id).phase='修复完成';find(id).new='PRESENT';find(id).newValue=old.get(id);state.results.repaired++;});alerts.forEach(id=>{find(id).phase='报警完成';state.results.alerted++;});skip.forEach(id=>{find(id).phase='相等跳过';state.results.skipped++;});state.tx=null;
    emit('done','事务处理确认完成','修复 '+changed.length+' 条、保留报警 '+alerts.length+' 条、跳过 '+skip.length+' 条。报警可靠性在真实接入中由业务事务/outbox 实现。');
    clear(batch);emit('release','释放已完成复核批','仅在事务与必要结果确认后回收 '+batch.length+' 条；各状态计数同步扣除。');
   }
  }
 }
 emit('init','初始化：50 条老表记录',scenarios[scene].desc+' Scan='+scanSize+'，复核批='+reviewSize+'，插入批='+insertSize+'，Memory 上限=50。');
 let cursor=0;
 while(true){const ids=Array.from(old.keys()).filter(id=>id>cursor).slice(0,scanSize);state.old.phase='扫描中';
  if(!ids.length){state.old.phase='扫描结束';emit('scanOld','老表扫描 EOF','成功读取空页，确认边界内已读完；不能把超时当 EOF。','scan');break;}
  rows(ids,'old');cursor=ids.at(-1);state.old.read=cursor;state.old.rows+=ids.length;state.old.phase='缓存满暂停';state.batch='老表扫描页 '+ids[0]+'…'+cursor;
  emit('scanOld','SCAN 老表：'+ids.length+' 条','当前页 '+ids[0]+'…'+cursor+'；按 shard 分组处理，先排空当前页，不继续扫描。','scan');
  emit('route','按目标 shard 分组','当前页共有 '+new Set(ids.map(route)).size+' 个目标分片；后续逐个处理，不并发发起查询。');reconcile(ids,false);if(failed)break;state.old.safe=cursor;state.old.batches++;emit('checkpoint','推进老表安全游标','当前页涉及的所有 shard 和复核批已完成，安全游标才推进到 '+cursor+'。');
 }
 if(!failed){for(let sh=1;sh<=shards;sh++){const t=state.tables[sh-1];let newCursor=0;while(true){const ids=Array.from(fresh.keys()).filter(id=>route(id)===sh&&id>newCursor).sort((a,b)=>a-b).slice(0,scanSize);t.phase='扫描中';if(!ids.length){t.phase='扫描结束';emit('scanNew','shard '+sh+' 扫描 EOF','空分片也需成功查询一次才能确认结束。','scan');break;}rows(ids,'new');newCursor=ids.at(-1);t.cursor=newCursor;t.rows+=ids.length;t.phase='缓存满暂停';state.batch='反向扫描 shard '+sh;emit('scanNew','反向 SCAN shard '+sh+'：'+ids.length+' 条','加载一页后暂停，完整批量查询老表并复核额外记录；已修复记录再次遇到会初比相等。','scan');reconcile(ids,true);if(failed)break;}if(failed)break;}}
 if(!failed){state.status='DONE';state.batch='全部排空';emit('finish','本轮完成','老表和全部 '+shards+' 个 shard 均扫描结束；Memory=0，无未决事务。额外记录全部保留。');}
 return {scene,meta:scenarios[scene],events,before,after:Object.fromEntries(fresh),old:Object.fromEntries(old),extras,shards};
}
global.ReconcileDemo={build,scenarios};
})(typeof globalThis!=='undefined'?globalThis:this);
