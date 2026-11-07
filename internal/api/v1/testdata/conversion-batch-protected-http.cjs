'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const {randomUUID}=require('node:crypto');
const {spawnSync}=require('node:child_process');
const {CommercialClient,ProtectedStore}=require('../reference/commercial-client.cjs');
(async()=>{
 const input=JSON.parse(fs.readFileSync(0,'utf8')),key=input.key||randomUUID();
 let store=new ProtectedStore(input.directory,key),lose=false,interrupt=false,posts=0;
 const request=async(path,method='GET',body,auth={token:input.token})=>{
  const headers={Authorization:'Bearer '+auth.token,'Content-Type':'application/json'};
  if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;
  if(auth.operation)headers['Operation-ID']=auth.operation;
  if(path==='/sync/operations'){
   posts++;const persisted=await store.load();
   for(const op of body.operations)assert.equal(persisted.pending[op.operation_id].state,'unknown');
   if(interrupt){headers['X-Batch-Interrupt']='yes';interrupt=false;}
  }
  const r=await fetch(input.url+path,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
  const result={status:r.status,...await r.json()};
  if(path==='/sync/operations'&&lose){lose=false;throw Error('lost_response');}
  return result;
 };
 let client=new CommercialClient(store,request);
 if(input.worker){
  if(input.lock)await assert.rejects(client.view(),{message:input.lock});
  else assert.deepEqual((await client.view()).pending,input.pending);
  return;
 }
 const restart=(pending,lock)=>{
  const r=spawnSync(process.execPath,[__filename],{input:JSON.stringify({...input,key,worker:true,pending,lock}),encoding:'utf8',timeout:30000});
  assert.equal(r.status,0,r.stdout+r.stderr);
  client=new CommercialClient(new ProtectedStore(input.directory,key),request);
 };
 const fixture=async(name,body)=>fetch(input.url+'/__fixture/'+name,{method:'POST',body:body&&JSON.stringify(body)});
 await client.initialize();await client.authenticate(input.token);
  const reversion=input.scenario.startsWith('batch_reversion_'),kind=reversion?'invoice':'estimate',action=reversion?'revert':'convert';
  const entered=input.scenario==='batch_fields'||input.scenario==='batch_authority_input';
  const values=doc=>reversion?{cycle_id:doc.cycle_id}:entered?{custom_fields:[{definition_id:input.fields.entered,value:'BATCH-ENTERED-SECRET'}]}:{};
  for(let i=0;i<3;i++){
   const creation={operation_id:randomUUID(),client_id:randomUUID(),job_id:input.job,customer_id:input.customer,title:'BATCH-CONVERSION-SECRET-'+i};
   if(entered)creation.custom_fields=[{definition_id:input.fields.source,value:'Paired source wins'}];
   const created=await request('/estimates','POST',creation,{token:input.creationToken});assert.equal(created.status,200);
   if(reversion)assert.equal((await request('/estimates/'+created.data.id+'/convert','POST',{operation_id:randomUUID(),job_id:input.job,revision:created.data.revision})).status,200);
  }
  await client.synchronize(kind);
 const docs=Object.values((await client.view()).documents);
  const proposed=values(docs[0]);
  const a=await client.stageBatch(docs[0].id,docs[0].revision,proposed,action,[]);
  if(entered)proposed.custom_fields[0].value='Caller mutated after staging';
  const b=await client.stageBatch(docs[1].id,docs[1].revision,values(docs[1]),action,[a]);
  const c=await client.stageBatch(docs[2].id,docs[2].revision,values(docs[2]),action,[]);
  const original=(await client.view()).pending;
  if(entered)assert.equal(original[a].intent.custom_fields[0].value,'BATCH-ENTERED-SECRET');
  for(const id of [a,b,c]){assert.equal(original[id].intent.operation_id,id);assert.equal(original[id].intent.job_id,input.job);assert(original[id].intent.occurred_at);assert.equal(original[id].batch.type,kind+'.'+action);}
 restart(original);
  for(const name of fs.readdirSync(input.directory))assert(!fs.readFileSync(input.directory+'/'+name).includes(Buffer.from('BATCH-CONVERSION-SECRET')));
  if(entered)for(const name of fs.readdirSync(input.directory))assert(!fs.readFileSync(input.directory+'/'+name).includes(Buffer.from('BATCH-ENTERED-SECRET')));
  if(input.scenario==='batch_authority_input'){
   const results=await client.submitBatch([a]);assert.equal(results[0].receipt.outcome,'forbidden');
   restart(null,'locked_review');assert(!(await store.load()).pending[a].intent);
   assert.equal((await request('/estimates/'+docs[0].legacy_id)).status,200);return;
  }
 await assert.rejects(client.submit(a),{message:'transport_identity_mismatch'});
 if(input.scenario==='batch_restore'){
  const changed=await fixture('change');
  await assert.rejects(client.synchronize('estimate'),{message:'locked_review'});restart(null,'locked_review');
  await client.authenticate((await changed.json()).token);await client.synchronize('estimate');
  await assert.rejects(client.submitBatch([a,b,c]),{message:'reconciliation_required'});
  for(const id of [a,b,c]){await client.reconcile(id);await client.synchronize('estimate');await client.acknowledgeUncertain(id);}
  const again=await fixture('change');await client.authenticate((await again.json()).token);await client.synchronize('estimate');
  await assert.rejects(client.repairBatch(a,docs[0].revision,{},[]),{message:'restore_reconciliation_required'});
  for(const id of [a,b,c]){await client.reconcile(id);await client.synchronize('estimate');await client.acknowledgeUncertain(id);}
  assert.equal(posts,0,'restore automatically resubmitted');
  const next=await client.repairBatch(a,docs[0].revision,{},[]);assert.notEqual(next,a);
  assert.deepEqual((await client.view()).pending[b].batch.depends_on,[a]);
  assert.equal((await client.submitBatch([next]))[0].receipt.outcome,'accepted');return;
 }
  if(input.scenario==='batch_reversion_stale'){
   assert.equal((await request('/invoices/'+docs[0].legacy_id,'PATCH',{operation_id:randomUUID(),job_id:input.job,revision:docs[0].revision,notes:'Intervening revision'})).status,200);
   const results=await client.submitBatch([a,b,c]);
   assert.equal(results[0].receipt.outcome,'stale_commercial_revision');assert.equal(results[1].receipt.outcome,'failed_dependency');assert.equal(results[2].receipt.outcome,'accepted');
   await client.synchronize(kind);restart((await client.view()).pending);
   const current=(await client.view()).documents[docs[0].id];
   const repaired=await client.repairBatch(a,current.revision,values(current),[]);assert.notEqual(repaired,a);
   assert.deepEqual((await client.view()).pending[b].batch.depends_on,[a]);
   assert.equal((await client.submitBatch([repaired]))[0].receipt.outcome,'accepted');
   assert.equal((await client.reconcile(b)).receipt.outcome,'failed_dependency');await client.synchronize(kind);
   const next=await client.repairBatch(b,docs[1].revision,values(docs[1]),[repaired]);assert.notEqual(next,b);
   restart((await client.view()).pending);assert.equal((await client.submitBatch([next]))[0].receipt.outcome,'accepted');return;
  }
  if(input.scenario==='batch_transient'||input.scenario==='batch_reversion_transient'){
  assert.equal((await fixture('fault',{ID:a})).status,204);
  const results=await client.submitBatch([a,b,c]);
  assert.equal(results[0].state,'unknown');assert.equal(results[1].receipt.outcome,'failed_dependency');assert.equal(results[2].receipt.outcome,'accepted');
  restart(null,'locked_review');assert.equal((await client.reconcile(a)).state,'pending');
   assert.equal((await fixture('clear')).status,204);await client.synchronize(kind);
  await new Promise(resolve=>setTimeout(resolve,6000));
  assert.equal((await client.submitBatch([a]))[0].receipt.outcome,'accepted');
   assert.equal((await client.reconcile(b)).receipt.outcome,'failed_dependency');await client.synchronize(kind);
   const next=await client.repairBatch(b,docs[1].revision,values(docs[1]),[a]);assert.notEqual(next,b);
  assert.equal((await client.submitBatch([next]))[0].receipt.outcome,'accepted');return;
 }
  if(input.scenario==='batch_interruption'||input.scenario==='batch_reversion_interruption'){
  interrupt=true;await assert.rejects(client.submitBatch([a,b]),/fetch failed/);restart(null,'locked_review');
   assert.equal((await client.reconcile(a)).receipt.outcome,'accepted');await client.synchronize(kind);
  await assert.rejects(client.submitBatch([c]),{message:'reconciliation_required'});
   assert.equal((await client.reconcile(b)).state,'pending');await client.synchronize(kind);
  assert.deepEqual((await client.view()).pending[b].intent,original[b].intent);
  assert((await client.submitBatch([b,c])).every(r=>r.receipt.outcome==='accepted'));return;
 }
 lose=true;await assert.rejects(client.submitBatch([a,b,c]),{message:'lost_response'});restart(null,'locked_review');
 const receipts=[];for(const id of [a,b,c])receipts.push((await client.reconcile(id)).receipt);
  assert(receipts.every(r=>r.outcome==='accepted'));
  if(input.scenario==='batch_fields'){
   for(const receipt of receipts){const invoice=(await request('/invoices/'+receipt.id)).data;
    assert.equal(invoice.custom_fields.find(f=>f.definition_id===input.fields.entered).value,'BATCH-ENTERED-SECRET');
    assert.equal(invoice.custom_fields.find(f=>f.definition_id===input.fields.paired).value,'Paired source wins');}
   return;
  }
 const wire={operation_id:a,...original[a].batch,commercial:{...original[a].intent}};delete wire.commercial.operation_id;
 const raw=async op=>{
  const r=await fetch(input.url+'/sync/operations',{method:'POST',headers:{Authorization:'Bearer '+input.token,'Content-Type':'application/json'},body:JSON.stringify({operations:[op]})});
  assert.equal(r.status,200);return (await r.json()).data.results[0];
 };
 assert.deepEqual((await raw(wire)).receipt,receipts[0]);
 assert.equal((await raw({...wire,commercial:{...wire.commercial,occurred_at:'2000-01-01T00:00:00Z'}})).error.code,'operation_intent_changed');
 input.directory+='/invoice';fs.mkdirSync(input.directory,{mode:0o700});store=new ProtectedStore(input.directory,key);client=new CommercialClient(store,request);
 await client.initialize();await client.authenticate(input.token);await client.synchronize('invoice');
 const invoice=Object.values((await client.view()).documents).find(d=>d.legacy_id===receipts[0].id);
 const revert=await client.stageBatch(invoice.id,invoice.revision,{cycle_id:invoice.cycle_id},'revert',[]);
 const obsolete=await client.stageBatch(invoice.id,invoice.revision,{cycle_id:invoice.cycle_id},'revert',[]);
 const pending=(await client.view()).pending;restart(pending);
 assert.equal(pending[revert].intent.cycle_id,receipts[0].cycle_id);
 lose=true;await assert.rejects(client.submitBatch([revert]),{message:'lost_response'});restart(null,'locked_review');
 const restored=(await client.reconcile(revert)).receipt;assert.equal(restored.outcome,'accepted');
 const source=(await request('/estimates/'+restored.id)).data;
 const later=(await request('/estimates/'+restored.id+'/convert','POST',{operation_id:randomUUID(),job_id:input.job,revision:source.revision})).data;
 assert.notEqual(later.cycle_id,invoice.cycle_id);
 await client.synchronize('invoice');
 assert(!(await client.view()).pending[obsolete].intent);
 await assert.rejects(client.repairBatch(obsolete,later.revision,{cycle_id:later.cycle_id},[]),{message:'review_required'});
 assert.equal((await request('/invoices/'+later.id)).status,200);
 console.log('protected conversion batch history, identity, encrypted restart and no descendant retarget PASS');
})().catch(e=>{console.error(e);process.exitCode=1;});
