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
  if(input.lock)await assert.rejects(client.view(),{message:input.lock});
  else assert.deepEqual((await client.view()).pending[input.id].intent,input.intent);
  return;
 }
 const restart=(id,intent,lock)=>{
  const r=spawnSync(process.execPath,[__filename],{input:JSON.stringify({...input,key,restart:true,id,intent,lock}),encoding:'utf8',timeout:30000});
  assert.equal(r.status,0,r.stdout+r.stderr);
 };
 let offline=false,lose=false,writes=0;
 const request=async(path,method='GET',body,auth={token:input.token})=>{
  assert(!offline,'staging contacted network');
  const headers={Authorization:'Bearer '+auth.token,'Content-Type':'application/json'};
  if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;
  if(auth.operation)headers['Operation-ID']=auth.operation;
  const r=await fetch(input.url+path,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
  return {status:r.status,...await r.json()};
 };
 const transport=async(path,method,body,auth)=>{
  const mutation=/\/(convert|revert-to-estimate)$/.test(path);
  if(mutation){writes++;assert.equal((await store.load()).pending[body.operation_id].state,'unknown');}
  const result=await request(path,method,body,auth);
  if(mutation&&lose){lose=false;throw Error('lost_response');}
  return result;
 };
 let client=new CommercialClient(store,transport);
 await client.initialize();await client.authenticate(input.token);
 const creation={operation_id:randomUUID(),client_id:randomUUID(),job_id:input.job,customer_id:input.customer,title:'PROTECTED-CONVERSION-SECRET'};
 if(input.scenario==='fields')creation.custom_fields=[{definition_id:input.fields.source,value:'Captured shared'},{definition_id:input.fields.original,value:'Original only'}];
 if(input.scenario==='settlement')creation.line_items=[{title:'Work',quantity:'1',unit_price:'100.00'}];
 const created=await request('/estimates','POST',creation,{token:input.creationToken});
 assert.equal(created.status,200);
 const path='/estimates/'+created.data.id;
 await client.synchronize('estimate');
 let view=await client.view(),target=Object.keys(view.documents)[0],source=view.documents[target];
 assert.equal(source.actions.convert,true);
 await assert.rejects(client.stageBatch(target,source.revision,{},'convert',[]),{message:'unsupported_batch_action'});
 offline=true;
 const stale=await client.stageConversion(target,source.revision,{});
 const original=(await client.view()).pending[stale].intent;
 assert.equal(original.job_id,input.job);assert.equal(original.revision,source.revision);assert.equal(original.operation_id,stale);
 assert(original.occurred_at);restart(stale,original);
 for(const name of fs.readdirSync(input.directory))assert(!fs.readFileSync(input.directory+'/'+name).includes(Buffer.from('PROTECTED-CONVERSION-SECRET')));
 offline=false;
 if(['origin_refund','settlement'].includes(input.scenario)){
  const converted=(await client.submit(stale)).receipt;assert.equal(converted.outcome,'accepted');
  const invoicePath='/invoices/'+converted.id;
  let invoice=(await request(invoicePath)).data;
  const choice=invoice.workflow.find(w=>w.category==='invoice:invoiced');
  assert.equal((await request(invoicePath+'/finalize','POST',{operation_id:randomUUID(),job_id:input.job,revision:invoice.revision,status_id:choice.id,meaning_revision:choice.meaning_revision})).status,200);
  const directory=input.directory+'/invoice';fs.mkdirSync(directory,{mode:0o700});
  let inv=new CommercialClient(new ProtectedStore(directory,key),request);await inv.initialize();await inv.authenticate(input.token);await inv.synchronize('invoice');
  let v=await inv.view(),target=Object.keys(v.documents)[0];invoice=v.documents[target];
  let operation=await inv.stageConversion(target,invoice.revision,{cycle_id:converted.cycle_id});
  const queued=[];
  if(input.scenario==='origin_refund')for(let i=0;i<3;i++)queued.push(await inv.stageConversion(target,invoice.revision,{cycle_id:converted.cycle_id}));
  const restartInvoice=async()=>{
   const intent=(await inv.view()).pending[operation].intent;
   const child=spawnSync(process.execPath,[__filename],{input:JSON.stringify({...input,directory,key,restart:true,id:operation,intent}),encoding:'utf8',timeout:30000});assert.equal(child.status,0,child.stderr);
   inv=new CommercialClient(new ProtectedStore(directory,key),request);
  };
  await restartInvoice();
  const financial=async action=>{const r=await fetch(input.url+'/__fixture/change',{method:'POST',body:JSON.stringify({action,invoice:converted.id})});assert.equal(r.status,204);};
  await financial('payment');
  assert.equal((await inv.submit(operation)).status,input.scenario==='origin_refund'?422:409);
  assert.equal((await inv.reconcile(operation)).receipt.outcome,input.scenario==='origin_refund'?'invalid_document':'stale_commercial_revision');
  if(input.scenario==='origin_refund'){
   const blocked=async()=>{
    const comparison=await inv.compare(operation);
    assert.equal(comparison.current.actions.revert,false);
    await assert.rejects(inv.repair(operation,comparison.current.revision,{cycle_id:converted.cycle_id}),{message:'review_required'});
    operation=queued.shift();await restartInvoice();
    assert.equal((await inv.submit(operation)).status,422);
    assert.equal((await inv.reconcile(operation)).receipt.outcome,'invalid_document');
    assert.equal((await request(invoicePath)).status,200,'blocked revert hid Invoice');
   };
   await blocked();await financial('refund');await blocked();await financial('reverse_refund');await blocked();
  }else{
   const comparison=await inv.compare(operation);assert.equal(comparison.current.settlement_locked,true);assert.equal(comparison.current.actions.revert,false);
   await assert.rejects(inv.repair(operation,comparison.current.revision,{cycle_id:converted.cycle_id}),{message:'review_required'});
  }
  await financial('reverse_payment');
  const comparison=await inv.compare(operation);
  await assert.rejects(inv.repair(operation,comparison.current.revision,{cycle_id:randomUUID()}),{message:'exact_cycle_review_required'});
  const repaired=await inv.repair(operation,comparison.current.revision,{cycle_id:converted.cycle_id});assert.notEqual(repaired,operation);operation=repaired;await restartInvoice();
  assert.equal((await inv.submit(operation)).receipt.outcome,'accepted');assert.equal((await request(invoicePath)).status,404);
  console.log('protected exact-cycle financial prerequisites and explicit unwind PASS');return;
 }
 if(input.scenario==='fields'){
  assert.equal((await client.submit(stale)).status,422);
  assert.equal((await client.reconcile(stale)).receipt.outcome,'invalid_document');
  let comparison=await client.compare(stale);assert.deepEqual(comparison.proposed,original);
  const values={custom_fields:[{definition_id:input.fields.paired,value:'Must not override source'},{definition_id:input.fields.entered,value:'ENCRYPTED-ENTERED-SECRET'}]};
  const repaired=await client.repair(stale,comparison.current.revision,values);assert.notEqual(repaired,stale);
  const intent=(await client.view()).pending[repaired].intent;restart(repaired,intent);
  values.custom_fields[1].value='Caller changed';assert.equal(intent.custom_fields[1].value,'ENCRYPTED-ENTERED-SECRET');
  for(const name of fs.readdirSync(input.directory))assert(!fs.readFileSync(input.directory+'/'+name).includes(Buffer.from('ENCRYPTED-ENTERED-SECRET')));
  const converted=(await client.submit(repaired)).receipt;assert.equal(converted.outcome,'accepted');
  const invoicePath='/invoices/'+converted.id;
  let invoice=(await request(invoicePath)).data;
  const field=(doc,id)=>doc.custom_fields.find(v=>v.definition_id===id)?.value;
  assert.equal(field(invoice,input.fields.paired),'Captured shared');assert.equal(field(invoice,input.fields.entered),'ENCRYPTED-ENTERED-SECRET');
  assert.equal((await request(invoicePath,'PATCH',{operation_id:randomUUID(),job_id:input.job,revision:invoice.revision,custom_fields:[{definition_id:input.fields.paired,value:'Current shared'}]})).status,200);
  const changed=await fetch(input.url+'/__fixture/change',{method:'POST'});const required=(await changed.json()).required;
  const directory=input.directory+'/invoice';fs.mkdirSync(directory,{mode:0o700});
  let inv=new CommercialClient(new ProtectedStore(directory,key),request);await inv.initialize();await inv.authenticate(input.token);await inv.synchronize('invoice');
  let v=await inv.view(),target=Object.keys(v.documents)[0];invoice=v.documents[target];
  const revert=await inv.stageConversion(target,invoice.revision,{cycle_id:converted.cycle_id});
  const revertIntent=(await inv.view()).pending[revert].intent;
  const child=spawnSync(process.execPath,[__filename],{input:JSON.stringify({...input,directory,key,restart:true,id:revert,intent:revertIntent}),encoding:'utf8',timeout:30000});assert.equal(child.status,0,child.stderr);
  inv=new CommercialClient(new ProtectedStore(directory,key),request);
  assert.equal((await inv.submit(revert)).receipt.outcome,'accepted');
  const recovered=(await request(path)).data;
  assert.equal(field(recovered,input.fields.source),'Current shared');assert.equal(field(recovered,input.fields.original),'Original only');
  assert.equal(field(recovered,input.fields.entered),undefined);assert.equal(field(recovered,required),undefined);
  assert.equal((await request(invoicePath)).status,404);
  console.log('protected required-input repair and captured retired pairing recovery PASS');return;
 }
 if(input.scenario!=='history'){
  const changed=await fetch(input.url+'/__fixture/change',{method:'POST'});
  if(input.scenario.startsWith('authority_')){
   await assert.rejects(client.submit(stale),{message:'authorization_purged'});
   restart(stale,null,'locked_review');
   await client.authenticate(input.token);
   const receipt=(await client.reconcile(stale)).receipt;
   assert.equal(receipt.outcome,'forbidden');
   assert(!JSON.stringify(receipt).includes('PROTECTED-CONVERSION-SECRET'));
   await client.authenticate(input.token);
   await client.synchronize('estimate');assert(!(await store.load()).pending[stale].intent);
   return;
  }
  await assert.rejects(client.synchronize('estimate'),{message:input.scenario==='revoke'?'authorization_purged':'locked_review'});
  restart(stale,null,'locked_review');assert.deepEqual((await store.load()).documents,{});
  if(input.scenario==='revoke'){assert(!(await store.load()).pending[stale].intent);assert.equal(writes,0);return;}
  await client.authenticate((await changed.json()).token);await client.synchronize('estimate');restart(stale,original);
  await assert.rejects(client.submit(stale),{message:'reconciliation_or_review_required'});
  await client.reconcile(stale);await client.synchronize('estimate');await client.acknowledgeUncertain(stale);
  if(input.scenario.startsWith('repeated_restore_')){
   const again=await fetch(input.url+'/__fixture/change',{method:'POST'}),token=(await again.json()).token;
   if(input.scenario.endsWith('_contact'))await assert.rejects(client.synchronize('estimate'),{message:'locked_review'});
   await client.authenticate(token);restart(stale,null,'locked_review');await client.synchronize('estimate');
   await assert.rejects(client.acknowledgeUncertain(stale),{message:'restore_reconciliation_required'});
   await assert.rejects(client.repair(stale,(await client.view()).documents[target].revision,{}),{message:'restore_reconciliation_required'});
   client=new CommercialClient(new ProtectedStore(input.directory,key),transport);
   await assert.rejects(client.repair(stale,(await client.view()).documents[target].revision,{}),{message:'restore_reconciliation_required'});
   await client.reconcile(stale);await client.synchronize('estimate');await client.acknowledgeUncertain(stale);
  }
  assert.equal(writes,0,'restore must never automatically replay');
  const repaired=await client.repair(stale,(await client.view()).documents[target].revision,{});assert.notEqual(repaired,stale);
  restart(repaired,(await client.view()).pending[repaired].intent);
  assert.equal((await client.submit(repaired)).receipt.outcome,'accepted');return;
 }
 for(const title of ['ABA intervening','PROTECTED-CONVERSION-SECRET']){
  const current=(await request(path)).data;
  assert.equal((await request(path,'PATCH',{operation_id:randomUUID(),job_id:input.job,revision:current.revision,title})).status,200);
 }
 assert.equal((await client.submit(stale)).status,409);
 assert.equal((await client.reconcile(stale)).receipt.outcome,'stale_commercial_revision');
 const comparison=await client.compare(stale);assert.deepEqual(comparison.proposed,original);assert.notEqual(comparison.current.revision,source.revision);
 const id=await client.repair(stale,comparison.current.revision,{});assert.notEqual(id,stale);
 const intent=(await client.view()).pending[id].intent;restart(id,intent);
 lose=true;await assert.rejects(client.submit(id),{message:'lost_response'});restart(id,null,'locked_review');
 client=new CommercialClient(new ProtectedStore(input.directory,key),transport);
 const first=(await client.reconcile(id)).receipt;assert.equal(first.outcome,'accepted');assert(first.cycle_id);
 assert.equal((await request(path)).status,404);
 // Each kind owns its own protected store, including its independent review.
 const invoiceDirectory=input.directory+'/invoice';fs.mkdirSync(invoiceDirectory,{mode:0o700});
 const invoiceStore=new ProtectedStore(invoiceDirectory,key);
 let invoiceClient=new CommercialClient(invoiceStore,async(path,method,body,auth)=>{
  const mutation=path.endsWith('/revert-to-estimate');
  if(mutation){assert.equal((await invoiceStore.load()).pending[body.operation_id].state,'unknown');writes++;}
  const result=await request(path,method,body,auth);
  if(mutation&&lose){lose=false;throw Error('lost_response');}return result;
 });
 await invoiceClient.initialize();await invoiceClient.authenticate(input.token);await invoiceClient.synchronize('invoice');
 view=await invoiceClient.view();const invoiceTarget=Object.keys(view.documents)[0],invoice=view.documents[invoiceTarget];
 assert.equal(invoice.cycle_id,first.cycle_id);
 const revert=await invoiceClient.stageConversion(invoiceTarget,invoice.revision,{cycle_id:invoice.cycle_id});
 const obsolete=await invoiceClient.stageConversion(invoiceTarget,invoice.revision,{cycle_id:invoice.cycle_id});
 const revertIntent=(await invoiceClient.view()).pending[revert].intent;
 assert.equal(revertIntent.cycle_id,first.cycle_id);
 const child=spawnSync(process.execPath,[__filename],{input:JSON.stringify({...input,directory:invoiceDirectory,key,restart:true,id:revert,intent:revertIntent}),encoding:'utf8',timeout:30000});assert.equal(child.status,0,child.stderr);
 lose=true;await assert.rejects(invoiceClient.submit(revert),{message:'lost_response'});
 invoiceClient=new CommercialClient(new ProtectedStore(invoiceDirectory,key),request);
 const restored=(await invoiceClient.reconcile(revert)).receipt;assert.equal(restored.outcome,'accepted');
 assert.equal((await request('/invoices/'+first.id)).status,404);
 await client.synchronize('estimate');source=(await client.view()).documents[target];
 const next=await client.stageConversion(target,source.revision,{});const second=(await client.submit(next)).receipt;
 assert.equal(second.outcome,'accepted');assert.notEqual(second.cycle_id,first.cycle_id);assert.notEqual(second.id,first.id);assert.notEqual(second.number,first.number);
 const replay=await request('/invoices/'+first.id+'/revert-to-estimate','POST',revertIntent);assert.equal(replay.status,200);assert.deepEqual(replay.data,restored);
 await invoiceClient.synchronize('invoice');
 await assert.rejects(invoiceClient.submit(obsolete),{message:'reconciliation_or_review_required'});
 assert(!(await invoiceClient.view()).pending[obsolete].intent,'lost original parent retained replayable intent');
 await assert.rejects(invoiceClient.repair(obsolete,(await request('/invoices/'+second.id)).data.revision,{cycle_id:second.cycle_id}),{message:'review_required'});
 await invoiceClient.synchronize('invoice');view=await invoiceClient.view();const later=Object.values(view.documents)[0],laterTarget=Object.keys(view.documents)[0];
 await assert.rejects(invoiceClient.stageConversion(laterTarget,later.revision,{cycle_id:first.cycle_id}),{message:'exact_cycle_review_required'});
 assert.equal((await request('/invoices/'+second.id)).status,200);
 assert.equal(writes,4);
 console.log('protected conversion: encrypted restart, ABA/new-ID repair, lost-response receipts, exact cycle, reconversion and history PASS');
})().catch(error=>{console.error(error);process.exitCode=1;});
