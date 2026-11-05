'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const {randomUUID}=require('node:crypto');
const {spawnSync}=require('node:child_process');
const {ServiceEntryClient,ProtectedStore}=require('../reference/service-entry-client.cjs');
(async()=>{
 const input=JSON.parse(fs.readFileSync(0,'utf8')),key=input.store_id||randomUUID();
 const store=new ProtectedStore(input.directory,key);
 if(input.restart){
  const restarted=new ServiceEntryClient(store,async()=>{throw Error('unexpected_network');});
  const locked=new RegExp(input.expected);
  await assert.rejects(restarted.view(),locked);
  await assert.rejects(restarted.repairServiceEntry(input.pending,{},[]),locked);
  console.log('Fresh-process protected exposure/repair lock PASS');return;
 }
 const restart=(pending,expected)=>{
  const r=spawnSync(process.execPath,[__filename],{input:JSON.stringify({...input,store_id:key,restart:true,pending,expected}),encoding:'utf8',timeout:30000});
  assert.equal(r.status,0,r.stdout+r.stderr);
 };
 let conflictObserved=false,failed=false,failSync=false;
 const transport=async(path,method,body,auth)=>{
  if(failSync&&path.startsWith('/sync/jobs'))throw Error('sync_unavailable');
  const headers={Authorization:'Bearer '+auth.token};
  if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;
  if(auth.operation)headers['Operation-ID']=auth.operation;
  if(body!==undefined)headers['Content-Type']='application/json';
  const r=await fetch(input.url+path,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
  const result={status:r.status,...await r.json()};
  if(path==='/sync/operations'&&result.data?.results?.some(r=>r.receipt?.code==='service_entry_conflict'))conflictObserved=true;
  return result;
 };
 let client=new ServiceEntryClient(store,transport);
 try{
  await client.initialize();await client.authenticate(input.token);await client.synchronize();
  let cache=(await client.view()).cache;
  const job=Object.keys(cache).find(j=>cache[j].masters.assets.some(a=>a.name==='Private pump'));
  const other=Object.keys(cache).find(j=>j!==job),asset=cache[job].masters.assets[0].id,otherAsset=cache[other].masters.assets[0].id;
  const content={service_date:input.date,summary:'PRIVATE-SERVICE-HISTORY',responsible_user_ids:[input.user]};
  const create=await client.stageServiceEntry(job,asset,content,[]);
  assert.equal((await client.submitBatch([create]))[0].receipt.code,'accepted');
  await client.synchronize(true);
  assert.equal((await client.leaseIndicator(job,asset)).value,'No');
  await client.readLeaseIndicators(job);
  cache=(await client.view()).cache;
  assert.equal(cache[job].masters.assets[0].service_entries[0].summary,content.summary);
  assert.ok(Object.keys(cache[job].lease_indicators).length);
  const directAsset=Object.keys(cache[job].lease_indicators)[0];
  assert.equal((await client.leaseIndicator(job,directAsset)).value,'No');
  const pending=await client.stageServiceEntry(job,asset,{...content,summary:'PRIVATE-STAGED-SUMMARY'},[]);
  const child=await client.stageServiceEntry(job,asset,{...content,summary:'PRIVATE-DESCENDANT'},[pending]);
  const otherPending=await client.stageServiceEntry(other,otherAsset,{...content,summary:'Other Job retained'},[]);
  const original=(await client.view()).pending[otherPending];
  for(const name of fs.readdirSync(input.directory)){
   if(fs.statSync(input.directory+'/'+name).isFile())assert(!fs.readFileSync(input.directory+'/'+name).includes(Buffer.from('PRIVATE-')),'plaintext sensitive cache');
  }
  assert.equal((await fetch(input.url+'/__fixture/relationship-loss',{method:'POST'})).status,204);
  const save=store.save.bind(store);
  store.save=async value=>{
   if(input.fail_write&&conflictObserved&&!failed){failed=true;throw Error('injected_authority_write_failure');}
   return save(value);
  };
  if(input.fail_write){
   await assert.rejects(client.submitBatch([pending,child]),/protected_store_locked/);
   assert.equal(failed,true);assert.equal(conflictObserved,true);
   client=new ServiceEntryClient(new ProtectedStore(input.directory,key),transport);
   await assert.rejects(client.view(),/store_busy/);
   await assert.rejects(client.synchronize(),/store_busy/);
   await assert.rejects(client.repairServiceEntry(pending,content,[]),/store_busy/);
   restart(pending,'store_busy');
   console.log('Real relationship conflict + failed durable withholding quarantines encrypted restart PASS');
   return;
  }
  const results=await client.submitBatch([pending,child]);
  assert.deepEqual(results.map(r=>r.receipt.code),['service_entry_conflict','failed_dependency']);
  // Neither the live client nor reconstruction can expose comparison or mint
  // new intent from the pre-contact relationship/capability snapshot.
  await assert.rejects(client.view(),/locked_review/);
  client=new ServiceEntryClient(new ProtectedStore(input.directory,key),transport);
  await assert.rejects(client.view(),/locked_review/);
  await assert.rejects(client.leaseIndicator(job,asset),/locked_review/);
  await assert.rejects(client.leaseIndicator(job,directAsset),/locked_review/);
  await assert.rejects(client.repairServiceEntry(pending,content,[]),/locked_review/);
  restart(pending,'locked_review');
  failSync=true;
  await assert.rejects(client.synchronize(),/sync_unavailable/);
  await assert.rejects(client.view(),/locked_review/);
  failSync=false;
  await client.synchronize();
  const view=await client.view();
  assert(!JSON.stringify(view).includes('PRIVATE-'));
  assert(!view.pending[pending].intent);assert(!view.pending[child].intent);
  assert.equal(view.pending[pending].receipt.operation_id,pending);
  assert.equal(view.pending[pending].receipt.code,'service_entry_conflict');
  assert.equal(view.pending[create].receipt.code,'accepted');
  assert.equal((await client.leaseIndicator(job,asset)).value,'Unavailable');
  assert.equal((await client.leaseIndicator(job,directAsset)).value,'Unavailable');
  assert.deepEqual(view.pending[otherPending],original);
  await assert.rejects(client.repairServiceEntry(pending,content,[]),/review_required/);
  assert.equal((await client.submitBatch([otherPending]))[0].receipt.code,'accepted');
  assert.equal((await client.reconcile(pending)).receipt.code,'service_entry_conflict');
  await assert.rejects(client.view(),/locked_review/);
  console.log('Real relationship conflict, encrypted restart, authorized resnapshot purge, safe receipt and other-Job preservation PASS');
 }finally{
  // Quarantine deliberately prevents normal client actions. Test teardown only
  // destroys its own encrypted fixture/key; it never reopens the stale cache.
  await store.destroy();
 }
})().catch(e=>{console.error(e);process.exitCode=1});
