'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const {randomUUID}=require('node:crypto');
const {spawnSync}=require('node:child_process');
const {CommercialClient,ProtectedStore}=require('../reference/commercial-client.cjs');
(async()=>{
 const input=JSON.parse(fs.readFileSync(0,'utf8')),key=input.key||randomUUID();
 const store=new ProtectedStore(input.directory,key);
 const secret='INVOICE-ONLY-DIRECT-SUCCESS-SECRET';
 if(input.restart){
  const client=new CommercialClient(store,async()=>{throw Error('unexpected_network');});
  const lock=input.persistence?'store_busy':'locked_review';
  await assert.rejects(client.view(),{message:lock});
  await assert.rejects(client.stageConversion(input.target,input.revision,input.values),{message:lock});
  if(input.persistence){await assert.rejects(client.initialize(),{message:lock});return;}
  const saved=await store.load();
  assert.deepEqual(saved.documents,{});assert.equal(saved.synced,false);
  assert.deepEqual(saved.pending[input.id].receipt,input.receipt);
  for(const id of input.queued){assert(!saved.pending[id].intent);assert(!saved.pending[id].reviewed);}
  assert(!JSON.stringify(saved).includes(secret));
  assert(!JSON.stringify(saved).includes('conversion_history'));
  return;
 }
 const request=async(path,method='GET',body,auth={token:input.token})=>{
  const headers={Authorization:'Bearer '+auth.token,'Content-Type':'application/json'};
  if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;
  if(auth.operation)headers['Operation-ID']=auth.operation;
  const r=await fetch(input.url+path,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
  return {status:r.status,...await r.json()};
 };
 const accepted=async(path,body)=>{const r=await request(path,'POST',body);assert.equal(r.status,200);assert.equal(r.data.outcome,'accepted');return r.data;};
 const created=await accepted('/estimates',{operation_id:randomUUID(),client_id:randomUUID(),job_id:input.job,customer_id:input.customer,title:'Direct success source',custom_fields:[{definition_id:input.fields.original,value:'Estimate only'}]});
 const estimatePath='/estimates/'+created.id;
 const entered={custom_fields:[{definition_id:input.fields.entered,value:secret}]};
 const first=await accepted(estimatePath+'/convert',{operation_id:randomUUID(),job_id:input.job,revision:created.revision,...entered});
 const reverting=input.scenario.startsWith('direct_revert'),persistence=input.scenario.endsWith('_persistence');
 const kind=reverting?'invoice':'estimate';
 const path=reverting?'/invoices/'+first.id:estimatePath;
 if(!reverting){
  const invoice=(await request('/invoices/'+first.id)).data;
  await accepted('/invoices/'+first.id+'/revert-to-estimate',{operation_id:randomUUID(),job_id:input.job,revision:invoice.revision,cycle_id:first.cycle_id});
 }
 let received;
 let client=new CommercialClient(store,async(...args)=>{
  const r=await request(...args);
  if(persistence&&/\/(convert|revert-to-estimate)$/.test(args[0])){
   assert.equal(r.status,200);assert.equal(r.data.outcome,'accepted');received=r.data;
   // Real filesystem denial after the server commits, before receipt/cache save.
   fs.chmodSync(input.directory,0o500);
  }
  return r;
 });
 await client.initialize();await client.authenticate(input.token);await client.synchronize(kind);
 const view=await client.view(),target=Object.keys(view.documents)[0],doc=view.documents[target];
 const history=await client.conversionHistory(target);assert(history.records.length>0);
 if(reverting)assert(doc.custom_fields.some(f=>f.definition_id===input.fields.entered&&f.value===secret));
 const values=reverting?{cycle_id:first.cycle_id}:entered;
 const id=await client.stageConversion(target,doc.revision,values);
 const queued=[await client.stageEdit(target,doc.revision,{notes:'Other queued private review'}),await client.stageBatch(target,doc.revision,values,reverting?'revert':'convert',[])];
 const original=(await client.view()).pending[id].intent;
 for(const name of fs.readdirSync(input.directory))assert(!fs.readFileSync(input.directory+'/'+name).includes(Buffer.from(secret)));
 if(persistence){
  await assert.rejects(client.submit(id),{message:'protected_store_locked'});
  fs.chmodSync(input.directory,0o700);
  assert.equal((await request(path)).status,404);
  assert(fs.statSync(input.directory+'/.writer').isFile(),'failed authority checkpoint must retain durable restart lock');
  await assert.rejects(client.view(),{message:'store_busy'});
  await assert.rejects(client.submit(queued[0]),{message:'store_busy'});
  const child=spawnSync(process.execPath,[__filename],{input:JSON.stringify({...input,key,restart:true,persistence,target,revision:doc.revision,values}),encoding:'utf8',timeout:30000});
  assert.equal(child.status,0,child.stdout+child.stderr);
  const receipt=await request('/commercial-operations/'+id);assert.equal(receipt.status,200);assert.deepEqual(receipt.data,received);
  const replay=await request(path+(reverting?'/revert-to-estimate':'/convert'),'POST',original);
  assert.equal(replay.status,200);assert.deepEqual(replay.data,received);
  console.log(input.scenario+' actual failed encrypted persistence remains locked across process restart; original server receipt preserved PASS');return;
 }
 const result=await client.submit(id);assert.equal(result.status,200);assert.equal(result.receipt.outcome,'accepted');
 assert.equal((await request(path)).status,404);
 await assert.rejects(client.view(),{message:'locked_review'});
 await assert.rejects(client.stageConversion(target,doc.revision,values),{message:'locked_review'});
 await assert.rejects(client.submit(queued[0]),{message:'locked_review'});
 await assert.rejects(client.submitBatch([queued[1]]),{message:'locked_review'});
 const child=spawnSync(process.execPath,[__filename],{input:JSON.stringify({...input,key,restart:true,target,revision:doc.revision,values,id,queued,receipt:result.receipt}),encoding:'utf8',timeout:30000});
 assert.equal(child.status,0,child.stdout+child.stderr);
 client=new CommercialClient(new ProtectedStore(input.directory,key),request);
 assert.deepEqual((await client.reconcile(id)).receipt,result.receipt);
 const replay=await request(path+(reverting?'/revert-to-estimate':'/convert'),'POST',original);
 assert.equal(replay.status,200);assert.deepEqual(replay.data,result.receipt);
 await client.synchronize(kind);
 const current=await client.view();assert(!current.documents[target]);
 for(const operation of queued){assert(!current.pending[operation].intent);assert(!current.pending[operation].reviewed);}
 if(reverting){const restored=(await request(estimatePath)).data;assert(!JSON.stringify(restored).includes(secret));}
 console.log(input.scenario+' ordinary HTTP200 durable parent/history/queued-review purge and original receipt replay PASS');
})().catch(e=>{console.error(e);process.exit(1);});
