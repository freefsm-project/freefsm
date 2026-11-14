'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const {randomUUID}=require('node:crypto');
const {spawnSync}=require('node:child_process');
const {DeliveryClient,ProtectedStore}=require('../reference/delivery-client.cjs');
(async()=>{
 const input=JSON.parse(fs.readFileSync(0,'utf8'));
 const store=new ProtectedStore(input.directory,input.key||randomUUID());
 let offline=false,loss=null,posts=0;
 const calls=[];
 const transport=async(path,method='GET',body,auth)=>{
  assert(!offline,'offline staging contacted server');calls.push([method,path]);
  const effect=method==='POST'&&(path.endsWith('/deliveries')||path==='/delivery-retries');
  if(effect){posts++;assert.equal((await store.load()).pending[body.operation_id].state,'unknown');if(loss==='before'){loss=null;throw Error('lost_request');}}
  const headers={Authorization:'Bearer '+auth.token,'Content-Type':'application/json'};
  if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;
  if(auth.operation)headers['Operation-ID']=auth.operation;
  const r=await fetch(input.url+path,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
  const result={status:r.status,...await r.json()};
  if(effect&&loss==='after'){loss=null;assert.equal(r.status,200);throw Error('lost_response');}
  return result;
 };
 const control=async action=>{const r=await fetch(input.url+'/__fixture/'+action,{method:'POST',headers:{Authorization:'Bearer '+input.token}});assert(r.ok);return r;};
 let client=new DeliveryClient(store,transport);
 if(input.restart){
  await client.initialize();assert.equal((await client.reconcile(input.id)).receipt.outcome,'accepted');assert.equal(posts,0);return;
 }
 try{
  await client.initialize();await client.authenticate(input.token);await client.synchronize('estimate');
  const target=Object.keys((await client.view()).documents)[0];
  const message={pdf_version_id:input.version,confirm_stale_pdf:false,to:['private-delivery@example.test'],cc:[],bcc:[],subject:'PRIVATE-DELIVERY-MESSAGE',text_body:'PRIVATE-DELIVERY-BODY',html_body:'',extra_file_ids:[]};
  const review=await client.reviewDelivery(target,message);
  let dependency=input.scenario==='dependency'?randomUUID():undefined;
  if(input.scenario==='dependency_failed'){
   dependency=randomUUID();const state=await store.load();
   const r=await transport('/estimates/'+review.document_id+'/pdf/generate','POST',{operation_id:dependency,job_id:review.job_id,source_token:'no-longer-reviewed',depends_on:[]},{token:state.token,epoch:state.contactEpoch});assert.equal(r.status,409);
  }
  offline=true;let id=await client.stageDelivery(target,review.review_token,dependency);offline=false;
  if(input.scenario.startsWith('retry_')){
   const receipt=(await client.submit(id)).receipt;if(input.scenario!=='retry_invalid_state'&&input.scenario!=='retry_cancelled')await control('fail');
   offline=true;id=await client.stageRetry(target,receipt.delivery_ids[0],'PRIVATE-RETRY-REASON');offline=false;
  }
  const original=structuredClone((await store.load()).pending[id].intent);
  for(const name of fs.readdirSync(input.directory)){
   const raw=fs.readFileSync(input.directory+'/'+name);
   for(const secret of ['PRIVATE-DELIVERY','private-delivery@example.test','PRIVATE-RETRY',input.token])assert(!raw.includes(Buffer.from(secret)));
  }
  const before=posts;
  if(input.scenario==='expiry'){
   client.now=()=>Date.now()+73*3600000;
   await assert.rejects(client.view(),/authorization_lease_expired/);await assert.rejects(client.submit(id),/authorization_lease_expired/);assert.equal(posts,before);return;
  }
  if(input.scenario==='parent_loss'||input.scenario==='retry_parent_loss'){
   await control('parent-loss');await client.synchronize('estimate');const e=(await client.view()).pending[id];
   assert(!e.intent&&!e.reviewed);await assert.rejects(client.repairDelivery(id,review.review_token),/review_required/);assert.equal(posts,before);return;
  }
  if(input.scenario==='restore'){
   const fresh=await (await control('restore')).json();await assert.rejects(client.submit(id),/locked_review/);
   client=new DeliveryClient(new ProtectedStore(input.directory,store.id),transport);
   await client.authenticate(fresh.token);await client.synchronize('estimate');
   assert.equal((await client.view()).pending[id].state,'restore_uncertain');
   await assert.rejects(client.repairDelivery(id,review.review_token),/restore_reconciliation_required/);
   assert.equal((await client.reconcile(id)).state,'restore_uncertain');
   await client.synchronize('estimate');await client.acknowledgeUncertain(id);
   const current=await client.reviewDelivery(target,message);const next=await client.repairDelivery(id,current.review_token);assert.notEqual(next,id);assert.equal(posts,before);
   assert.equal((await client.submit(next)).receipt.outcome,'accepted');return;
  }
  if(input.scenario==='revoked'){
   await control('revoke');await assert.rejects(client.submit(id),/authorization_purged/);const s=await store.load();assert(!s.token&&!s.pending[id].intent&&!s.pending[id].reviewed);return;
  }
  if(input.scenario==='denial'||input.scenario==='retry_denial'){
   await control('deny');await assert.rejects(client.submit(id),/authorization_purged/);
   assert(!(await store.load()).pending[id].intent);await client.authenticate(input.token);
   assert.equal((await client.reconcile(id)).receipt.outcome,'forbidden');assert(!(await store.load()).pending[id].intent);return;
  }
  if(input.scenario==='retry_invalid_state'||input.scenario==='retry_cancelled'){
   if(input.scenario==='retry_cancelled')await control('cancel');
   assert.equal((await client.submit(id)).status,409);assert.equal((await client.reconcile(id)).receipt.outcome,input.scenario==='retry_cancelled'?'cancelled':'invalid_state');
   await client.synchronize('estimate');
   if(input.scenario==='retry_invalid_state'){
    await control('fail');const next=await client.repairRetry(id,'Explicit repair');assert.notEqual(next,id);
    assert.equal((await store.load()).pending[next].intent.delivery_id,original.delivery_id);assert.equal((await client.submit(next)).receipt.outcome,'accepted');
   }return;
  }
  if(input.scenario==='dependency'||input.scenario==='dependency_failed'){
   assert.equal((await client.submit(id)).status,409);assert.equal((await client.reconcile(id)).receipt.outcome,input.scenario==='dependency'?'invalid_dependency':'dependency_failed');
   await client.synchronize('estimate');const changed={...message,subject:'Explicitly changed message',to:['changed@example.test']};
   const fresh=await client.reviewDelivery(target,changed);const next=await client.repairDelivery(id,fresh.review_token);assert.notEqual(next,id);
   assert.deepEqual((await store.load()).pending[id].intent,original);assert.equal((await client.submit(next)).receipt.outcome,'accepted');return;
  }
  loss=input.scenario.endsWith('_absent')?'before':'after';
  await assert.rejects(client.submit(id),/lost_/);assert.equal(posts,before+1);
  client=new DeliveryClient(new ProtectedStore(input.directory,store.id),transport);await client.initialize();
  assert.deepEqual((await store.load()).pending[id].intent,original);
  if(input.scenario==='create_lost'||input.scenario==='retry_lost'){
   const child=spawnSync(process.execPath,[__filename],{input:JSON.stringify({...input,key:store.id,id,restart:true}),encoding:'utf8',timeout:30000});
   assert.equal(child.status,0,child.stdout+child.stderr);
  }
  if(input.scenario==='scope_receipt')await control('scope-loss');
  calls.length=0;const result=await client.reconcile(id);assert.deepEqual(calls[0],['GET','/delivery-operations/'+id]);assert.equal(posts,before+1);
  if(input.scenario.endsWith('_absent')){
   assert.equal(result.state,'unreceived');await assert.rejects(client.retryUnreceived(id),/review_required/);
   assert.equal((await client.retryUnreceived(id,{confirm:true})).receipt.outcome,'accepted');assert.equal(posts,before+2);
  }else{
   assert.equal(result.receipt.outcome,'accepted');assert.deepEqual(Object.keys(result.receipt).sort(),['delivery_ids','operation_id','outcome']);
   if(input.scenario==='scope_receipt'){await client.synchronize('estimate');assert.deepEqual((await client.view()).documents,{});assert(!(await store.load()).pending[id].intent);}
  }
 }finally{await client.logout();assert(!fs.existsSync(input.directory+'/cache'));}
 console.log('PASS protected Delivery '+input.scenario);
})().catch(e=>{console.error(e);process.exit(1);});
