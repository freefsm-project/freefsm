'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const {randomUUID}=require('node:crypto');
const {ServiceEntryClient,ProtectedStore}=require('../reference/service-entry-client.cjs');
(async()=>{
 const input=JSON.parse(fs.readFileSync(0,'utf8'));
 const key=randomUUID(),store=new ProtectedStore(input.directory,key);
 let offline=false,lose=false;
 const transport=async(path,method,body,auth)=>{
  if(offline)throw Error('offline');
  const headers={Authorization:'Bearer '+auth.token};
  if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;
  if(auth.operation)headers['Operation-ID']=auth.operation;
  if(body!==undefined)headers['Content-Type']='application/json';
  const r=await fetch(input.url+path,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
  const result={status:r.status,...await r.json()};
  if(lose&&path==='/sync/operations'){lose=false;throw Error('lost_response');}
  return result;
 };
 let client=new ServiceEntryClient(store,transport);
 try {
  await client.initialize();await client.authenticate(input.token);await client.synchronize();
  let cache=(await client.view()).cache;
  const job=Object.keys(cache)[0],asset=cache[job].masters.assets[0].id;
  const content={service_date:input.date,summary:'ENCRYPTED-SERVICE-SECRET',responsible_user_ids:[input.user]};
  offline=true;
  const create=await client.stageServiceEntry(job,asset,content,[]);
  client=new ServiceEntryClient(new ProtectedStore(input.directory,key),transport);
  assert.equal((await client.view()).pending[create].state,'pending');
  for(const name of fs.readdirSync(input.directory)){
   if(fs.statSync(input.directory+'/'+name).isFile())assert(!fs.readFileSync(input.directory+'/'+name).includes(Buffer.from(content.summary)),'plaintext service cache');
  }
  offline=false;lose=true;
  await assert.rejects(client.submitBatch([create]),/lost_response/);
  assert.equal((await client.view()).pending[create].state,'unknown');
  assert.equal((await client.reconcile(create)).receipt.code,'accepted');
  await client.synchronize();
  cache=(await client.view()).cache;
  let entry=cache[job].masters.assets[0].service_entries[0];
  assert.equal(entry.summary,content.summary);assert.equal(entry.can_revise,true);
  assert.equal(entry.job_id,job);assert.equal(entry.asset_id,asset);
  const reviewed={...content,entry_id:entry.id,revision:entry.revision,summary:'Reviewed staged correction'};
  const stale=await client.stageServiceEntry(job,asset,reviewed,[]);
  const blocked=await client.stageServiceEntry(job,asset,{...content,summary:'Explicit descendant'},[stale]);
  const independent=await client.stageServiceEntry(job,asset,{...content,summary:'Independent entry'},[]);
  const direct=async(summary,revision)=>{
   const r=await transport(`/jobs/${job}/assets/${asset}/service-entries/${entry.id}`,'POST',{...content,summary,revision,link_id:cache[job].masters.assets[0].service_entry_context.link_id,operation_id:randomUUID()},{token:input.token});
   assert.equal(r.status,200,JSON.stringify(r));
  };
  await direct('Intervening summary',entry.revision);
   if(input.drift==='ABA'){
    await client.synchronize(true);
    entry=(await client.view()).cache[job].masters.assets[0].service_entries[0];
    await direct(content.summary,entry.revision);
   }
   const original=(await client.view()).pending[stale].intent;
   const results=await client.submitBatch([stale,blocked,independent]);
   assert.deepEqual(results.map(r=>r.receipt.code),['service_entry_conflict','failed_dependency','accepted']);
   await assert.rejects(client.view(),/locked_review/);
   client=new ServiceEntryClient(new ProtectedStore(input.directory,key),transport);
   await assert.rejects(client.repairServiceEntry(stale,reviewed,[]),/locked_review/);
   await client.synchronize();
   assert.deepEqual((await client.view()).pending[stale].intent,original);
   assert.equal((await client.view()).pending[blocked].state,'terminal');
  entry=(await client.view()).cache[job].masters.assets[0].service_entries.find(e=>e.id===entry.id);
  await assert.rejects(client.repairServiceEntry(stale,reviewed,[]),/review_required/);
  const repaired=await client.repairServiceEntry(stale,{...reviewed,revision:entry.revision},[]);
  const repairedChild=await client.repairServiceEntry(blocked,{...content,summary:'Explicit descendant'},[repaired]);
  assert.notEqual(repaired,stale);assert.notEqual(repairedChild,blocked);
  assert.deepEqual((await client.submitBatch([repaired,repairedChild])).map(r=>r.receipt.code),['accepted','accepted']);
  await client.synchronize(true);
   assert.equal((await client.view()).cache[job].masters.assets[0].service_entries.length,3);
   assert.match((await client.leaseIndicator(job,asset)).label,/Last synced: No — as of/);
   const uncertain=await client.stageServiceEntry(job,asset,{...content,summary:'Restore-only pending'},[]);
   const beforeRestore=(await client.view()).pending[uncertain].intent;
   const restored=await (await fetch(input.url+'/__fixture/restore',{method:'POST'})).json();
   await assert.rejects(client.synchronize(),/locked_review/);
   client=new ServiceEntryClient(new ProtectedStore(input.directory,key),transport);
   await assert.rejects(client.view(),/locked_review/);
   await client.authenticate(restored.token);await client.synchronize();
   assert.deepEqual((await client.view()).pending[uncertain].intent,beforeRestore);
   await assert.rejects(client.repairServiceEntry(uncertain,content,[]),/restore_reconciliation_required/);
   assert.equal((await client.reconcile(uncertain)).state,'restore_uncertain');
   await client.synchronize();await client.acknowledgeUncertain(uncertain);
   const restoredRepair=await client.repairServiceEntry(uncertain,{...content,summary:'Explicit restore review'},[]);
   assert.notEqual(restoredRepair,uncertain);
   assert.equal((await client.submitBatch([restoredRepair]))[0].receipt.code,'accepted');
   const pending=await client.stageServiceEntry(job,asset,{...content,summary:'Never retarget'},[]);
  assert.equal((await fetch(input.url+'/__fixture/revoke',{method:'POST'})).status,204);
  assert.equal((await client.submitBatch([pending]))[0].receipt.code,'not_found');
  client=new ServiceEntryClient(new ProtectedStore(input.directory,key),transport);
  const lost=await client.view();assert(!lost.cache[job]);assert(!lost.pending[pending].intent);
   assert.equal((await client.reconcile(restoredRepair)).receipt.code,'accepted');
   console.log('Encrypted Service Entry snapshot/delta, restart, lost response, '+input.drift+', independent batch, authorized new-ID repair, restore-only review, bounded indicator and assignment purge PASS');
 }finally{await client.logout();}
})().catch(e=>{console.error(e);process.exitCode=1});
