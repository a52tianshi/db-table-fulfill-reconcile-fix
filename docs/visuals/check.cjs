/* Deterministic assertions for the offline design simulation. No DB access. */
const assert=require('node:assert/strict');require('./simulation.js');
for(const scene of Object.keys(ReconcileDemo.scenarios)){
 const t=ReconcileDemo.build(scene),last=t.events.at(-1).state;
 assert.equal(t.old[50],'old-50');
 for(const e of t.events){const s=e.state;assert(s.memory.length<=50,scene+' capacity');assert.equal(new Set(s.memory.map(r=>r.id)).size,s.memory.length,scene+' unique keys');assert(s.old.safe<=s.old.read,scene+' checkpoint');if(s.tx){assert(s.tx.rows.length<=8,'review limit');assert.equal(new Set(s.tx.rows.map(id=>s.memory.find(r=>r.id===id).shard)).size,1,'one shard per tx');}assert.equal(Object.values(s.io).reduce((a,b)=>a+b,0),t.events.slice(0,t.events.indexOf(e)+1).filter(v=>v.db).length,'one IO action per DB event');}
 if(['lookupFail','rollback','cancel'].includes(scene)){assert.notEqual(last.status,'DONE');assert.equal(last.old.safe,0);assert.deepEqual(t.before,t.after,'no committed writes');assert.equal(last.results.repaired,0);}
 else{assert.equal(last.status,'DONE',scene);assert.equal(last.old.safe,50);assert.equal(last.memory.length,0);assert(last.tables.every(s=>s.phase==='扫描结束'));if(scene!=='alert')for(const [k,v] of Object.entries(t.old))assert.equal(t.after[k],v,scene+' converge');}
 for(const id of t.extras)assert.equal(t.after[id],t.before[id],'extra preserved');
 if(scene==='alert'){assert.deepEqual(t.before,t.after);assert.equal(last.io.insert+last.io.update,0);}
 for(let i=1;i<t.events.length;i++){const e=t.events[i],previous=t.events[i-1];if(e.db==='update')assert.equal(e.state.tx.staged.length-previous.state.tx.staged.length,1,'each UPDATE stages exactly one key');}
 if(scene==='different')assert.equal(last.io.update,50,'50 differences require 50 single-row UPDATEs');
 if(scene==='equal'){assert.equal(last.io.begin,0);assert.equal(last.io.insert+last.io.update,0);}
 if(scene==='stale')assert(last.results.skipped>=1);
 if(scene==='uncertain'){assert(t.events.some(e=>e.state.status==='UNKNOWN'));assert(t.events.some(e=>e.node==='confirm'));}
 if(scene==='tail')assert.deepEqual(t.events.filter(e=>e.node==='scanOld'&&/SCAN/.test(e.title)).map(e=>e.state.memory.length),[17,17,16]);
 if(scene==='mixed'){const first=t.events.find(e=>e.node==='scanOld');assert.equal(first.state.memory.length,50);assert.deepEqual(Array.from({length:5},(_,i)=>first.state.memory.filter(r=>r.shard===i+1).length),[10,10,10,10,10]);assert.equal(last.results.repaired,18);assert.equal(last.results.alerted,5);}
 console.log(scene+': '+t.events.length+' steps, peak='+last.peak+', status='+last.status);
}
console.log('All 13 scenarios verified.');
