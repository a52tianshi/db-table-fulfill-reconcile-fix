(function(){
'use strict';
const $=id=>document.getElementById(id), mode=document.body.dataset.view;
const labels=['等待配对','等待初比','初比相等','等待复核攒批','复核批就绪','事务复核中','待批量插入','待逐条更新','等待报警','写入中','等待提交','修复完成','报警完成','相等跳过','提交结果未知','失败','取消'];
let trace,index=0,timer=null;
const esc=x=>String(x??'—').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
function stop(){if(timer){clearInterval(timer);timer=null;}$('play').textContent='自动播放';}
function load(){stop();trace=ReconcileDemo.build($('scenario').value);index=0;$('scenario-note').textContent=trace.meta.desc;$('shard-filter').innerHTML='<option value="all">全部 shard</option>'+Array.from({length:trace.shards},(_,i)=>'<option value="'+(i+1)+'">shard '+(i+1)+'</option>').join('');$('seek').max=trace.events.length-1;render();}
function render(){
 const e=trace.events[index],s=e.state;window.demoSnapshot={index,event:e,trace};
 $('position').textContent=(index+1)+' / '+trace.events.length;$('seek').value=index;$('previous').disabled=index===0;$('next').disabled=index===trace.events.length-1;
 $('event-title').textContent=e.title;$('event-detail').textContent=e.detail;$('action').textContent=e.db?'唯一 DB 动作：'+e.db.toUpperCase():'本地动作：'+e.node;$('action').className='pill '+(e.db?'db':'local');
 $('task-status').textContent=s.status;$('batch').textContent=s.batch;$('tx').textContent=s.tx?'shard '+s.tx.shard+' · '+s.tx.phase+' · 暂存 '+s.tx.staged.length+' 条':'无活动事务';
 $('read').textContent=s.old.read||'起点';$('safe').textContent=s.old.safe||'起点';$('peak').textContent=s.peak+' / 50';
 const filter=$('shard-filter').value,phase=$('state-filter').value;
 const rows=s.memory.filter(r=>(filter==='all'||String(r.shard)===filter)&&(!phase||r.phase===phase));
 const scoped=s.memory.filter(r=>filter==='all'||String(r.shard)===filter),counts=Object.fromEntries(labels.map(l=>[l,0]));scoped.forEach(r=>counts[r.phase]=(counts[r.phase]||0)+1);
 $('memory-total').textContent=scoped.length;$('memory-counts').innerHTML=labels.map(l=>'<span class="tag '+(counts[l]?'nonzero':'')+'">'+l+' <b>'+counts[l]+'</b></span>').join('');
 $('memory-rows').innerHTML=rows.length?rows.map(r=>'<tr><td>'+r.id+'</td><td>'+r.shard+'</td><td>'+r.old+'<small>'+esc(r.oldValue)+'</small></td><td>'+r.new+'<small>'+esc(r.newValue)+'</small></td><td>'+(r.oldScan?'老表扫描 ':'')+(r.newScan?'新表扫描':'')+'</td><td>'+r.phase+'</td><td>'+esc(r.kind)+'</td></tr>').join(''):'<tr><td colspan="7">当前筛选下没有驻留记录</td></tr>';
 $('shards').innerHTML=s.tables.map(t=>'<div class="shard '+(t.phase==='扫描结束'?'finished':'')+'"><b>shard '+t.shard+'</b><span>'+t.phase+'</span><small>游标 '+(t.cursor||'起点')+' · 已读 '+t.rows+' 条</small></div>').join('');
 $('old-phase').textContent=s.old.phase+' · 已读 '+s.old.rows+' · 已安全完成页 '+s.old.batches;
 $('results').innerHTML=[['初比相等',s.results.equal],['复核跳过',s.results.skipped],['修复确认',s.results.repaired],['保留报警',s.results.alerted]].map(([l,n])=>'<span class="tag">'+l+' <b>'+n+'</b></span>').join('');
 $('io').innerHTML=Object.entries(s.io).map(([l,n])=>'<span class="tag">'+l+' <b>'+n+'</b></span>').join('');$('db-total').textContent=Object.values(s.io).reduce((a,b)=>a+b,0);
 $('log').innerHTML=trace.events.slice(Math.max(0,index-11),index+1).map((v,i)=>'<li class="'+(v===e?'current':'')+'"><span>'+(Math.max(0,index-11)+i+1)+'</span><b>'+esc(v.title)+'</b><small>'+(v.db?'DB / '+v.db:'内存 / 调度')+'</small></li>').join('');
 $('grid').innerHTML=Array.from({length:50},(_,i)=>{const id=i+1,r=s.memory.find(x=>x.id===id);let cls='unread',title='尚未扫描';if(r){cls=/失败|取消|未知/.test(r.phase)?'bad':/相等|完成/.test(r.phase)?'finished':/配对|初比/.test(r.phase)?'loaded':'candidate';title=r.phase+' / shard '+r.shard;}else if(id<=s.old.read){cls='released';title='已处理并释放（可在历史步骤查看）';}return '<span class="cell '+cls+'" title="'+title+'">'+id+'</span>';}).join('');
 if(mode==='flow'){document.querySelectorAll('[data-node]').forEach(n=>n.classList.toggle('active',n.dataset.node===e.node));$('flow-current').textContent=e.title;}
 if(index===trace.events.length-1)stop();
}
$('scenario').innerHTML=Object.entries(ReconcileDemo.scenarios).map(([k,v])=>'<option value="'+k+'">'+v.label+'</option>').join('');$('state-filter').innerHTML='<option value="">所有记录状态</option>'+labels.map(l=>'<option>'+l+'</option>').join('');
$('scenario').onchange=load;$('shard-filter').onchange=render;$('state-filter').onchange=render;
$('next').onclick=()=>{stop();if(index<trace.events.length-1)index++;render();};$('previous').onclick=()=>{stop();if(index>0)index--;render();};$('reset').onclick=()=>{stop();index=0;render();};$('end').onclick=()=>{stop();index=trace.events.length-1;render();};$('seek').oninput=()=>{stop();index=Number($('seek').value);render();};
$('play').onclick=()=>{if(timer){stop();return;}if(index===trace.events.length-1)index=0;timer=setInterval(()=>{if(index<trace.events.length-1){index++;render();}else stop();},350);$('play').textContent='暂停';};
if(mode==='flow'){document.querySelectorAll('[data-node]').forEach(n=>{n.setAttribute('tabindex','0');n.setAttribute('role','button');function choose(){const hits=trace.events.map((e,i)=>e.node===n.dataset.node?i:-1).filter(i=>i>=0);if(!hits.length){$('flow-note').textContent='当前场景不会经过此分支。失败、回滚、取消或未知提交可切换上方场景查看。';return;}stop();index=hits.find(i=>i>index)??hits[0];$('flow-note').textContent='已跳到此节点的实际演示步骤；再次点击可查看下次经过。';render();}n.onclick=choose;n.onkeydown=event=>{if(event.key==='Enter'||event.key===' '){event.preventDefault();choose();}};});}
load();
})();
