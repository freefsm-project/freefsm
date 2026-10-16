'use strict';
const assert=require('node:assert/strict');
const {randomUUID}=require('node:crypto');
const fs=require('node:fs/promises');
const os=require('node:os');
const path=require('node:path');
const {execFileSync}=require('node:child_process');
const {OfflineClocks,ProtectedStore}=require('../reference/offline-clocks-client.cjs');
(async()=>{
 let input='';for await(const chunk of process.stdin)input+=chunk;
 const {url,token,job,scenario}=JSON.parse(input);
 const directory=await fs.mkdtemp(path.join(os.tmpdir(),'clock83-'));await fs.chmod(directory,0o700);
 const store=new ProtectedStore(directory,randomUUID());
 let lose=false,deltaRequests=0;
 const transport=async(p,method='GET',body,auth={})=>{
  if(p.startsWith('/sync/clocks?')&&p.includes('&cursor='))deltaRequests++;
  const headers={'Authorization':'Bearer '+auth.token,'Content-Type':'application/json'};
  if(auth.operation)headers['Operation-ID']=auth.operation;
  if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;
  if(lose&&p==='/sync/operations'){headers['X-Clock-Lose-Response']='yes';lose=false;}
  const r=await fetch(url+p,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
  const data=await r.json();
  return {status:r.status,...data};
 };
 let client=new OfflineClocks(store,transport);
 try{
  await client.initialize();await client.authenticate(token);await client.synchronize();
  if(scenario==='cli'){
   const child=path.join(directory,'cli');await fs.mkdir(child,{mode:0o700});const childID=randomUUID();
   const invoke=(action,fields={})=>{
    const output=execFileSync(process.execPath,[path.join(__dirname,'../reference/offline-notes.cjs')],{input:JSON.stringify({directory:child,store_id:childID,server:url+'/api/v1',workflow:'clocks',action,...fields}),encoding:'utf8',timeout:45000});
    return output.trim()?JSON.parse(output):{};
   };
   try{
    invoke('initialize');invoke('authenticate',{token});invoke('synchronize');
    const inside=invoke('stage-clock',{clock_action:'in',clock:{},depends_on:[]});assert.equal(inside.state,'pending');
    assert.equal(invoke('submit-batch',{operation_ids:[inside.operation_id]})[0].receipt.code,'accepted');
    invoke('synchronize');const entry=Object.values(invoke('view').clocks)[0];
    const outside=invoke('stage-clock',{clock_action:'out',clock:{entry_id:entry.entry_id},depends_on:[inside.operation_id]});
    invoke('submit-batch',{operation_ids:[outside.operation_id]});invoke('synchronize-delta');assert.equal(Object.values(invoke('view').clocks)[0].state,'closed');assert.equal(invoke('reconcile',{operation_id:outside.operation_id}).receipt.code,'accepted');
    console.log('PASS executable Clock workflow across fresh protected processes and production HTTP/schema');
   }finally{invoke('logout');await fs.rm(child,{recursive:true,force:true});}
   return;
  }
  if(scenario==='dependencies'){
   const inside=await client.stageClock('in',{},[]);await client.submitBatch([inside]);await client.synchronize();
   const entry=Object.values((await client.view()).clocks)[0];
   const parent=await client.stageClock('out',{entry_id:entry.entry_id},[randomUUID()]);
   const child=await client.stageClock('in',{},[parent]);
   const outcomes=await client.submitBatch([parent,child]);assert.equal(outcomes[0].receipt.code,'invalid_dependency');assert.equal(outcomes[1].receipt.code,'failed_dependency');
   const old=(await client.view()).pending;
   const repairedParent=await client.repairDependencies(parent,[]);await client.submitBatch([repairedParent]);
   assert.deepEqual((await client.view()).pending[child].batch.depends_on,[parent]);
   const repairedChild=await client.repairDependencies(child,[repairedParent]);const repaired=(await client.view()).pending;
   assert.equal(repaired[repairedParent].receipt.entry_ref,entry.entry_id);
   assert.equal(repaired[repairedChild].intent.clock.occurred_at,old[child].intent.clock.occurred_at);
   assert.equal((await client.submitBatch([repairedChild]))[0].receipt.code,'accepted');
   await assert.rejects(()=>client.repairDependencies(repairedChild,[]),/review_required/);
   await client.synchronize();const clocks=Object.values((await client.view()).clocks);assert.equal(clocks.length,2);assert.equal(clocks.filter(e=>e.state==='active').length,1);
   assert.equal(Date.parse(clocks.find(e=>e.state==='closed').clock_out),Date.parse(old[parent].intent.clock.occurred_at));
   assert.equal(Date.parse(clocks.find(e=>e.state==='active').clock_in),Date.parse(old[child].intent.clock.occurred_at));
   console.log('PASS explicit dependency repair retains original clock occurrence/target and never rewrites descendants');return;
  }
  if(scenario==='identity'||scenario==='solo'){
   const inside=await client.stageClock('in',{},[]);const result=await client.submitBatch([inside]);assert.equal(result[0].receipt?.code,'accepted',JSON.stringify(result));await client.synchronize();
   const entry=Object.values((await client.view()).clocks)[0];const expected=scenario==='identity'?'9007199254740993':'1';assert.equal(entry.entry_id,expected);
   const outside=await client.stageClock('out',{entry_id:entry.entry_id},[inside]);await client.submitBatch([outside]);
   assert.equal((await client.reconcile(outside)).receipt.entry_ref,expected);
   await client.synchronize();assert.equal(Object.values((await client.view()).clocks)[0].state,'closed');
   console.log('PASS '+scenario+' clock identity survives staging, exact out and receipt');return;
  }
  if(['gps-shape','gps-denial','gps-null'].includes(scenario)){
   const gps=scenario==='gps-null'?null:{latitude:51.5,longitude:-0.12,captured_at:scenario==='gps-shape'?'invalid-time':new Date().toISOString()};
   const original=await client.stageClock('in',{gps},[]);
   if(scenario==='gps-denial')await fetch(url+'/__fixture/revoke-gps',{method:'POST'});
   await client.submitBatch([original]);const before=(await client.view()).pending[original];assert.equal(before.state,'terminal');
   const repaired=await client.submitWithoutLocation(original,[]);const after=(await client.view()).pending[repaired];
   assert.equal(after.intent.clock.occurred_at,before.intent.clock.occurred_at);assert.equal(after.intent.clock.gps,undefined);assert.notEqual(repaired,original);
   assert.equal((await client.submitBatch([repaired]))[0].receipt.code,'accepted');
   console.log('PASS explicit location-free repair for '+scenario+' retains original occurrence/new reviewed ID');return;
  }
  if(scenario==='denial'){
   const denied=await client.stageClock('in',{job_id:job,notes:'private staged intent'},[]);
   await fetch(url+'/__fixture/revoke-time',{method:'POST'});
   await assert.rejects(()=>client.submitBatch([denied]),/authorization_purged/);
   const state=await store.load();assert.equal(state.pending[denied].intent,undefined);assert.deepEqual(state.clocks,{});
   console.log('PASS terminal clock denial refreshes authority and purges revoked sensitive intent');return;
  }
  const bad=await client.stageClock('in',{job_id:job,gps:{latitude:91,longitude:0,captured_at:new Date().toISOString()}},[]);
  await client.submitBatch([bad]);
  const before=(await client.view()).pending[bad];assert.equal(before.state,'terminal');assert.equal(before.receipt.code,'invalid_gps');
  const repaired=await client.submitWithoutLocation(bad,[]);assert.notEqual(repaired,bad);
  const after=(await client.view()).pending[repaired];assert.equal(after.intent.clock.occurred_at,before.intent.clock.occurred_at);assert.equal(after.intent.clock.gps,undefined);
  lose=true;await assert.rejects(()=>client.submitBatch([repaired]),/fetch failed/);
  client=new OfflineClocks(new ProtectedStore(directory,store.id),transport);
  assert.equal((await client.view()).pending[repaired].state,'unknown');
  assert.equal((await client.reconcile(repaired)).receipt.code,'accepted');
  await fetch(url+'/__fixture/lose-job',{method:'POST'});
  await client.synchronize();
  const view=await client.view();const entry=Object.values(view.clocks)[0];assert.equal(entry.job.title,'Job 0');assert.equal(entry.state,'active');
  const out=await client.stageClock('out',{entry_id:Number(entry.entry_id),gps:{latitude:51.5,longitude:-0.12,accuracy:0,captured_at:new Date().toISOString()}},[repaired]);
  await client.submitBatch([out]);await client.synchronize(true);assert.equal(deltaRequests,1);
  const closed=Object.values((await client.view()).clocks)[0];assert.equal(closed.state,'closed');assert.equal(closed.gps[0].accuracy,0);assert.equal(closed.history.length,2);
  assert.equal((await client.reconcile(out)).receipt.code,'accepted');
  await assert.rejects(()=>client.submitWithoutLocation(out,[]),/review_required/);
  const uncertain=await client.stageClock('in',{},[]);
  await fetch(url+'/__fixture/restore',{method:'POST'});
  await assert.rejects(()=>client.synchronize(),/locked_review/);
  const fresh=await (await fetch(url+'/__fixture/token')).json();
  await client.authenticate(fresh.token);await client.synchronize();
  assert.equal((await client.view()).pending[uncertain].state,'restore_uncertain');
  assert.equal((await client.reconcile(uncertain)).state,'restore_uncertain');
  await assert.rejects(()=>client.submitBatch([uncertain]),/reconciliation_or_review_required/);
  await fetch(url+'/__fixture/revoke',{method:'POST'});
  await assert.rejects(()=>client.synchronize(),/authorization_purged/);
  const purged=await store.load();assert.deepEqual(purged.clocks,{});assert.deepEqual(purged.clockJobs,{});assert.equal(purged.clockLease,null);
  console.log('PASS encrypted clock restart, receipt-first loss, original-occurrence GPS omission, exact close-out after assignment loss, retained history and separate GPS');
 }finally{await client.logout();await fs.rm(directory,{recursive:true,force:true});}
})().catch(e=>{console.error(e);process.exit(1);});
