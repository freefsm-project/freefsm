'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const {randomUUID}=require('node:crypto');
const {spawnSync}=require('node:child_process');
const {CommercialClient,ProtectedStore}=require('../reference/commercial-client.cjs');
(async()=>{
 const input=JSON.parse(fs.readFileSync(0,'utf8')),key=input.key||randomUUID();
 const store=new ProtectedStore(input.directory,key);
 if(input.restart){
  const client=new CommercialClient(store,async()=>{throw Error('unexpected_network');});
  if(input.lock)await assert.rejects(client.view(),new RegExp(input.lock));
  else assert.deepEqual((await client.view()).pending[input.id].intent,input.intent);
  return;
 }
 const restart=(id,intent,lock)=>{
  const r=spawnSync(process.execPath,[__filename],{input:JSON.stringify({...input,key,restart:true,id,intent,lock}),encoding:'utf8',timeout:30000});
  assert.equal(r.status,0,r.stdout+r.stderr);
 };
  let offline=false,lose=false,policyUnavailable=false;
 const transport=async(path,method='GET',body,auth)=>{
  if(offline)throw Error('offline');
  const headers={Authorization:'Bearer '+auth.token};
  if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;
  if(auth.operation)headers['Operation-ID']=auth.operation;
  if(body!==undefined)headers['Content-Type']='application/json';
  const r=await fetch(input.url+path,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
   const result={status:r.status,...await r.json()};
   // Inject a gateway failure after real policy contact, not a transport exception.
   if(policyUnavailable&&path==='/offline/policy')return {status:503,error:{code:'service_unavailable'}};
  if(lose&&method==='PATCH'){lose=false;throw Error('lost_response');}
  return result;
 };
 const client=new CommercialClient(store,transport);
 try{
  await client.initialize();await client.authenticate(input.token);await client.synchronize(input.kind);
  const first=await client.view(),target=Object.keys(first.documents)[0],snapshot=first.documents[target];
  assert(snapshot.revision);assert(snapshot.workflow.length);assert.equal(snapshot.actions.edit,true);
  const values={notes:'ENCRYPTED-COMMERCIAL-SECRET'};
  offline=true;const id=await client.stageEdit(target,snapshot.revision,values);values.notes='mutated caller';
  const original=(await client.view()).pending[id].intent;
  assert.equal(original.notes,'ENCRYPTED-COMMERCIAL-SECRET');assert.equal(original.customer_id,snapshot.customer_id);
  restart(id,original);
  for(const name of fs.readdirSync(input.directory))assert(!fs.readFileSync(input.directory+'/'+name).includes(Buffer.from(original.notes)));
  offline=false;
  if(input.scenario==='scope_loss'){
   lose=true;await assert.rejects(client.submit(id),/lost_response/);
   assert.equal((await fetch(input.url+'/__fixture/change',{method:'POST'})).status,204);
   const receipt=(await client.reconcile(id)).receipt;assert.equal(receipt.outcome,'accepted');
   assert(!JSON.stringify(receipt).includes(original.notes));restart(id,null,'locked_review');
   await assert.rejects(client.synchronize(input.kind),/authorization_purged/);
   const s=await store.load();assert.deepEqual(s.documents,{});assert(!s.pending[id].intent);assert(!s.pending[id].reviewed);
   return;
  }
   if(input.scenario==='failed_comparison'){
    policyUnavailable=true;await assert.rejects(client.submit(id),/reauthorization_unavailable/);
    restart(id,null,'locked_review');
    assert.deepEqual((await store.load()).documents,{});
    policyUnavailable=false;await client.synchronize(input.kind);
    assert.deepEqual((await client.view()).pending[id].intent,original);
   // Seed observations produced by another adapter in the same protected owner.
   await store.exclusive(async()=>{const s=await store.load();s.notes={private:'INHERITED-NOTES'};s.clocks={private:'INHERITED-CLOCK'};s.pending.inherited={state:'pending',intent:{field_notes:'INHERITED-PROPOSAL'}};await store.save(s);});
   offline=true;await assert.rejects(client.compare(id),/offline/);restart(id,null,'locked_review');
   const s=await store.load();assert.deepEqual(s.documents,{});assert.deepEqual(s.notes,{});assert.deepEqual(s.clocks,{});
   offline=false;await client.synchronize(input.kind);assert.deepEqual((await client.view()).pending[id].intent,original);
   assert(!(await store.load()).pending.inherited.intent);return;
  }
  if(input.scenario==='lifecycle'){
   await client.discard(id);
   const transition=async(category,action='status')=>{
    const doc=(await client.view()).documents[target],choice=doc.workflow.find(w=>w.category===category);
    const operation=await client.stageLifecycle(target,doc.revision,{id:choice.id,meaning_revision:choice.meaning_revision,category:choice.category},action);
    restart(operation,(await client.view()).pending[operation].intent);
    const result=await client.submit(operation);assert.equal(result.receipt?.outcome,'accepted');
    await client.synchronize(input.kind,true);return (await client.view()).documents[target];
   };
   if(input.kind==='invoice'){
    assert(!snapshot.workflow.some(w=>['invoice:paid','invoice:partially_paid'].includes(w.category)));
    let doc=await transition('invoice:invoiced','finalize');assert.equal(doc.effective_category,'invoice:paid');assert.equal(doc.settlement_locked,false);
    const revision=doc.revision;doc=await transition('invoice:invoiced');assert.equal(doc.revision,revision,'desired-state no-op changed commercial attribution');
    doc=await transition('invoice:void');assert.equal(doc.effective_category,'invoice:void');
    doc=await transition('invoice:sent');assert.equal(doc.effective_category,'invoice:paid');
    doc=await transition('invoice:draft');assert.equal(doc.effective_category,'invoice:draft');
   }else{
    for(const choice of snapshot.workflow){const doc=await transition(choice.category);assert.equal(doc.manual_category,choice.category);assert.equal(doc.actions.edit,true);}
   }
   return;
  }
  if(input.scenario==='restore'||input.scenario==='revoke'){
   const changed=await fetch(input.url+'/__fixture/change',{method:'POST'});
   await assert.rejects(client.synchronize(input.kind),input.scenario==='restore'?/locked_review/:/authorization_purged/);
   restart(id,null,'locked_review');
   const persisted=await store.load();assert.deepEqual(persisted.documents,{});
   if(input.scenario==='revoke'){assert(!persisted.pending[id].intent);assert(!persisted.token);return;}
   const fresh=await changed.json();await client.authenticate(fresh.token);await client.synchronize(input.kind);
   assert.deepEqual((await client.view()).pending[id].intent,original);
   await assert.rejects(client.submit(id),/reconciliation_or_review_required/);
   await client.reconcile(id);await client.synchronize(input.kind);await client.acknowledgeUncertain(id);
   const next=await client.repair(id,(await client.view()).documents[target].revision,{notes:original.notes});
   assert.notEqual(next,id);assert.equal((await client.submit(next)).receipt.outcome,'accepted');return;
  }
  // Another accepted, disjoint edit invalidates the originally reviewed whole revision.
  const other=await transport('/'+input.kind+'s/'+snapshot.legacy_id,'PATCH',{operation_id:randomUUID(),job_id:snapshot.job_id,revision:snapshot.revision,terms:'Concurrent terms'},{token:input.token});
  assert.equal(other.status,200);
  await client.synchronize(input.kind,true);assert.deepEqual((await client.view()).pending[id].intent,original);
  const rejected=await client.submit(id);assert.equal(rejected.status,409);restart(id,null,'locked_review');
  assert.equal((await client.reconcile(id)).receipt.outcome,'stale_commercial_revision');
  await client.synchronize(input.kind);
  const comparison=await client.compare(id);assert.equal(comparison.current.terms,'Concurrent terms');assert.equal(comparison.proposed.notes,original.notes);
  const next=await client.repair(id,comparison.current.revision,{notes:original.notes});assert.notEqual(next,id);
  lose=true;await assert.rejects(client.submit(next),/lost_response/);restart(next,null,'locked_review');
  await assert.rejects(client.submit(next),/locked_review/);
  const receipt=(await client.reconcile(next)).receipt;assert.equal(receipt.outcome,'accepted');
  await client.synchronize(input.kind);const current=(await client.view()).documents[target];
  assert.equal(current.notes,original.notes);assert.equal(current.terms,'Concurrent terms');
  const replay=await transport('/'+input.kind+'s/'+snapshot.legacy_id,'PATCH',{...original,operation_id:next,revision:comparison.current.revision},{token:input.token});
  assert.deepEqual(replay.data,receipt);
  client.now=()=>Date.now()+73*3600000;await assert.rejects(client.view(),/authorization_lease_expired/);restart(next,null,'authorization_lease_expired');
  console.log('Protected commercial '+input.kind+' conflict, restart, explicit repair and receipt replay PASS');
 }finally{await store.destroy();}
})().catch(e=>{console.error(e);process.exitCode=1;});
