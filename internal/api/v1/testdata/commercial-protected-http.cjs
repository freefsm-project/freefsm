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
  let offline=false,lose=false,policyUnavailable=false,race=false,writes=0;
 const transport=async(path,method='GET',body,auth)=>{
  if(offline)throw Error('offline');
  const headers={Authorization:'Bearer '+auth.token};
  if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;
  if(auth.operation)headers['Operation-ID']=auth.operation;
   if(body!==undefined)headers['Content-Type']='application/json';
   if(race&&path.startsWith('/invoices/')&&method!=='GET')headers['X-Settlement-Race']='yes';
  const r=await fetch(input.url+path,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
   const result={status:r.status,...await r.json()};
   // Inject a gateway failure after real policy contact, not a transport exception.
   if(policyUnavailable&&path==='/offline/policy')return {status:503,error:{code:'service_unavailable'}};
  if(lose&&method==='PATCH'){lose=false;throw Error('lost_response');}
  return result;
 };
  const client=new CommercialClient(store,async(path,method,body,auth)=>{
   if(method==='PATCH'||method==='POST'&&/\/(status|finalize)$/.test(path)){
    writes++;assert.equal((await store.load()).pending[body.operation_id].state,'unknown','write not durably unknown before network');
   }
   return transport(path,method,body,auth);
  });
 try{
  await client.initialize();await client.authenticate(input.token);await client.synchronize(input.kind);
  const first=await client.view(),target=Object.keys(first.documents)[0],snapshot=first.documents[target];
  assert(snapshot.revision);assert(snapshot.workflow.length);assert.equal(snapshot.actions.edit,true);
   const values={notes:'ENCRYPTED-COMMERCIAL-SECRET'};
   if(input.scenario==='authority_pricing')values.line_items=[{title:'Captured work',quantity:'1',unit_price:'100.00'}];
   if(input.itemID)values.line_items=[{item_id:input.itemID,title:'Reviewed snapshot',quantity:'1',unit_price:'9.00',taxable:false}];
   for(const anchor of ['job_id','customer_id'])await assert.rejects(client.stageEdit(target,snapshot.revision,{[anchor]:snapshot[anchor]+1}),{message:'review_required'});
   offline=true;let id=await client.stageEdit(target,snapshot.revision,values);values.notes='mutated caller';
   if(input.scenario==='revision_finalize'){
    await client.discard(id);
    const choice=snapshot.workflow.find(w=>w.category==='invoice:invoiced');
    id=await client.stageLifecycle(target,snapshot.revision,{id:choice.id,meaning_revision:choice.meaning_revision,category:choice.category},'finalize');
   }
  const original=(await client.view()).pending[id].intent;
   if(input.scenario!=='revision_finalize'){
    assert.equal(original.notes,'ENCRYPTED-COMMERCIAL-SECRET');assert.equal(original.customer_id,snapshot.customer_id);
   }
  restart(id,original);
   for(const name of fs.readdirSync(input.directory))assert(!fs.readFileSync(input.directory+'/'+name).includes(Buffer.from('ENCRYPTED-COMMERCIAL-SECRET')));
   offline=false;
   if(input.scenario==='authority_pricing_snapshot'){
    assert(snapshot.totals);assert.equal((await fetch(input.url+'/__fixture/change',{method:'POST'})).status,204);
    await assert.rejects(client.synchronize(input.kind,true),{message:'authorization_purged'});restart(id,null,'locked_review');
    let persisted=await store.load();assert.deepEqual(persisted.documents,{});assert(!persisted.pending[id].intent);assert(!persisted.pending[id].reviewed);
    await client.authenticate(input.token);await client.synchronize(input.kind);
    const current=(await client.view()).documents[target];
    for(const key of ['line_items','financial','tax_rate','totals','arithmetic_version'])assert(!(key in current));
    assert.equal(current.actions.edit,true,'pricing facet loss removed independent nonfinancial Edit authority');
    const next=await client.stageEdit(target,current.revision,{notes:'Authorized nonfinancial edit'});assert.equal((await client.submit(next)).receipt.outcome,'accepted');
    return;
   }
   if(input.scenario==='transient'){
    assert.equal((await fetch(input.url+'/__fixture/fault',{method:'POST',body:JSON.stringify({ID:id})})).status,204);
    const result=await client.submit(id);assert.equal(result.status,503);restart(id,null,'locked_review');
    assert.equal((await client.reconcile(id)).state,'pending');await client.synchronize(input.kind);
    assert.deepEqual((await client.view()).pending[id].intent,original);assert.equal((await client.view()).documents[target].revision,snapshot.revision);
    assert.equal((await fetch(input.url+'/__fixture/clear',{method:'POST'})).status,204);
    const deadline=(await store.load()).pending[id].retryNotBefore;client.now=()=>Math.max(Date.now(),deadline+1);
    assert.equal((await client.submit(id)).receipt.outcome,'accepted');await client.synchronize(input.kind);
    assert.equal((await client.view()).documents[target].notes,original.notes);assert.equal(writes,2);return;
   }
   if(['purge_storage','restore_storage'].includes(input.scenario)){
    lose=true;await assert.rejects(client.submit(id),{message:'lost_response'});restart(id,null,'locked_review');
    await fetch(input.url+'/__fixture/change',{method:'POST'});
    const save=store.save.bind(store);store.save=async s=>{if(!s.token)throw Error('injected authority write failure');return save(s);};
    const sent=writes;await assert.rejects(client.reconcile(id),{message:'protected_store_locked'});
    assert.equal(store.quarantined,true);assert(fs.existsSync(input.directory+'/.writer'));assert.equal(writes,sent);
    const retained=await store.load();assert.equal(retained.pending[id].state,'unknown');assert.deepEqual(retained.pending[id].intent,original);
    restart(id,null,'store_busy');return;
   }
   if(input.scenario.startsWith('input_')){
    assert.equal((await fetch(input.url+'/__fixture/input',{method:'POST'})).status,204);
    const result=await client.submit(id);
    if(input.scenario==='input_archive'){
     assert.equal(result.status,409);restart(id,null,'locked_review');assert.equal((await client.reconcile(id)).receipt.outcome,'commercial_conflict');
     const comparison=await client.compare(id);assert.deepEqual(comparison.proposed,original);assert.equal(comparison.current.revision,snapshot.revision);
     const next=await client.repair(id,comparison.current.revision,{notes:original.notes,line_items:[]});assert.notEqual(next,id);restart(next,(await client.view()).pending[next].intent);
     assert.equal((await client.submit(next)).receipt.outcome,'accepted');
    }else assert.equal(result.receipt.outcome,'accepted');
    await client.synchronize(input.kind,true);let current=(await client.view()).documents[target];
    assert.equal(current.job_id,snapshot.job_id);assert.equal(current.customer_id,snapshot.customer_id);assert.equal(current.totals.total,input.scenario==='input_archive'?'0.00':'9.00');
    if(input.scenario==='input_price'){
     assert.equal(current.line_items[0].unit_price,'9.00');
     const invalid=await client.stageEdit(target,current.revision,{line_items:[{title:'Invalid negative total',quantity:'1',unit_price:'-1.00'}]});
     assert.equal((await client.submit(invalid)).status,422);assert.equal((await client.reconcile(invalid)).receipt.outcome,'invalid_document');
     await client.synchronize(input.kind);assert.equal((await client.view()).documents[target].revision,current.revision);await client.discard(invalid);
    }
    return;
   }
   if(input.scenario.startsWith('race_')){
    await client.discard(id);
    const editing=input.scenario.includes('_edit_'),settlementFirst=input.scenario.endsWith('_settlement_first');
    let operation;
    if(editing)operation=await client.stageEdit(target,snapshot.revision,{notes:'Accepted lower total',line_items:[{title:'Work',quantity:'1',unit_price:'10.00'}]});
    else{
     const choice=snapshot.workflow.find(w=>w.category==='invoice:void');
     operation=await client.stageLifecycle(target,snapshot.revision,{id:choice.id,meaning_revision:choice.meaning_revision,category:choice.category},'status');
    }
    const intent=(await client.view()).pending[operation].intent;restart(operation,intent);
    race=true;const result=await client.submit(operation);race=false;
    assert.equal(result.status,settlementFirst?409:200);
    if(settlementFirst){restart(operation,null,'locked_review');assert.equal((await client.reconcile(operation)).receipt.outcome,'stale_commercial_revision');}
    else assert.equal(result.receipt.outcome,'accepted');
    await client.synchronize(input.kind);const current=(await client.view()).documents[target];
    assert.equal(current.settlement_locked,settlementFirst);
    assert.equal(current.totals.total,!settlementFirst&&editing?'10.00':'100.00');
    assert.equal(current.notes,!settlementFirst&&editing?'Accepted lower total':snapshot.notes);
    assert.equal(current.manual_category,!settlementFirst&&!editing?'invoice:void':'invoice:invoiced');
    assert.equal(current.effective_category,settlementFirst?'invoice:partially_paid':current.manual_category);
    if(settlementFirst){const comparison=await client.compare(operation);assert.deepEqual(comparison.proposed,intent);await client.discard(operation);}
    return;
   }
   if(input.scenario.startsWith('authority_')){
    assert.equal((await fetch(input.url+'/__fixture/change',{method:'POST'})).status,204);
    // The original pending operation is submitted before a new snapshot: cached
    // authority must never authorize acceptance at the production commit seam.
    const grantLoss=['authority_pricing','authority_edit','authority_pricebook'].includes(input.scenario);
    if(grantLoss){
     await assert.rejects(client.submit(id),{message:'authorization_purged'});
     const purged=await store.load();assert.deepEqual(purged.documents,{});assert(!purged.pending[id].intent);assert(!purged.token);
    }else{
     const result=await client.submit(id);assert.equal(result.status,404);assert.equal(result.code,'not_found');
    }
    restart(id,null,'locked_review');
    if(grantLoss)await client.authenticate(input.token);
    const receipt=(await client.reconcile(id)).receipt;assert.equal(receipt.outcome,grantLoss?'forbidden':'not_found');
    assert(!JSON.stringify(receipt).includes(original.notes));
    if(grantLoss)await client.authenticate(input.token);
    try{await client.synchronize(input.kind);}catch(error){assert.match(error.message,/authorization_purged|resnapshot_required/);}
    const persisted=await store.load();
    for(const doc of Object.values(persisted.documents||{})){
     if(input.scenario==='authority_pricing')for(const key of ['line_items','financial','tax_rate','totals','arithmetic_version'])assert(!(key in doc));
     if(['authority_edit','authority_assignment','authority_archive'].includes(input.scenario))assert.equal(doc.actions.edit,false);
    }
    if(['authority_edit','authority_assignment','authority_archive'].includes(input.scenario))await assert.rejects(client.stageEdit(target,snapshot.revision,{notes:'Force save'}),/review_required|locked_review|authorization/);
    assert(!persisted.pending[id].intent,'authority denial retained replayable original proposal');
    return;
   }
   if(input.scenario.startsWith('settlement_')){
    await client.discard(id);
    const setup=await fetch(input.url+'/__fixture/fields?action=create',{method:'POST'});assert.equal(setup.status,200);const fields=await setup.json();
    const changes=[{title:'Blocked'},{notes:'Blocked'},{terms:'Blocked'},{invoice_date:'2026-01-01'},{due_date:'2026-02-01'},{line_items:[]},{tax_rate:'1'},{custom_fields:[{definition_id:fields.text,value:'Blocked'}]},{adjustments:{discount:{kind:'fixed',value:'1.00'}}}];
    const operations=[];
    for(const change of changes)operations.push(await client.stageEdit(target,snapshot.revision,change));
    for(const choice of snapshot.workflow)operations.push(await client.stageLifecycle(target,snapshot.revision,{id:choice.id,meaning_revision:choice.meaning_revision,category:choice.category},'status'));
    const intents=(await client.view()).pending;
    assert.equal((await fetch(input.url+'/__fixture/settle',{method:'POST'})).status,204);
    for(const operation of operations){
     restart(operation,intents[operation].intent);
     const result=await client.submit(operation);assert.equal(result.status,409);
     restart(operation,null,'locked_review');assert.equal((await client.reconcile(operation)).receipt.outcome,'stale_commercial_revision');
     const comparison=await client.compare(operation);assert.deepEqual(comparison.proposed,intents[operation].intent);
     assert.equal(comparison.current.settlement_locked,true);assert.equal(comparison.current.manual_category,'invoice:invoiced');assert.equal(comparison.current.effective_category,'invoice:partially_paid');assert.equal(comparison.current.totals.total,'100.00');
     await client.discard(operation);
    }
    await client.synchronize(input.kind,true);const current=(await client.view()).documents[target];
    for(const action of ['edit','status','finalize'])assert.equal(current.actions[action],false);
    await assert.rejects(client.stageEdit(target,current.revision,{notes:'Force save'}),/review_required/);
    assert.equal(current.notes,snapshot.notes);assert.equal(current.terms,snapshot.terms);
    return;
   }
   if(input.scenario.startsWith('semantic_')){
    await client.discard(id);
    const choice=snapshot.workflow.find(w=>w.id===input.semanticID);
    const selection={id:choice.id,meaning_revision:choice.meaning_revision,category:choice.category};
    const operation=await client.stageLifecycle(target,snapshot.revision,selection,'status');
    const intent=(await client.view()).pending[operation].intent;restart(operation,intent);
    const changed=await fetch(input.url+'/__fixture/semantics',{method:'POST'});assert.equal(changed.status,200);
    const replacement=await changed.json();
    await client.synchronize(input.kind,true);
    let current=(await client.view()).documents[target];
    assert.equal(current.revision,snapshot.revision,'unselected vocabulary change changed document');
    assert.deepEqual((await client.view()).pending[operation].intent,intent);restart(operation,intent);
    const result=await client.submit(operation);
    if(input.scenario==='semantic_rename'){
     assert.equal(result.receipt.outcome,'accepted');
    }else{
     assert.equal(result.status,409);restart(operation,null,'locked_review');
     assert.equal((await client.reconcile(operation)).receipt.outcome,'commercial_conflict');
     const comparison=await client.compare(operation);assert.deepEqual(comparison.proposed,intent);
     await assert.rejects(client.repair(operation,comparison.current.revision,selection),/workflow_reselection_required/);
     const fresh=comparison.current.workflow.find(w=>w.id===replacement.id);
     const next=await client.repair(operation,comparison.current.revision,{id:fresh.id,meaning_revision:fresh.meaning_revision,category:fresh.category});
     assert.notEqual(next,operation);restart(next,(await client.view()).pending[next].intent);
     assert.equal((await client.submit(next)).receipt.outcome,'accepted');
    }
    await client.synchronize(input.kind,true);current=(await client.view()).documents[target];
    assert.equal(current.status_id,replacement.id);
    assert.equal(current.manual_category,input.scenario==='semantic_category'?'estimate:rejected':'estimate:accepted');
    return;
   }
   if(input.scenario==='changed_fields'){
    await client.discard(id);
    const setup=await fetch(input.url+'/__fixture/fields?action=create',{method:'POST'});assert.equal(setup.status,200);const fields=await setup.json();
    const refresh=async()=>{await client.synchronize(input.kind,true);return (await client.view()).documents[target];};
    const patch=async(values,want=200)=>{
     const before=await refresh(),operation=await client.stageEdit(target,before.revision,values),intent=(await client.view()).pending[operation].intent;
     restart(operation,intent);const result=await client.submit(operation);assert.equal(result.status,want,JSON.stringify(result));
     if(want!==200){
      restart(operation,null,'locked_review');assert.equal((await client.reconcile(operation)).receipt.outcome,'invalid_document');
      const comparison=await client.compare(operation);assert.deepEqual(comparison.proposed,intent);assert.equal(comparison.current.revision,before.revision);await client.discard(operation);
     }
     return refresh();
    };
    const value=(name,v)=>({custom_fields:[{definition_id:fields[name],value:v}]});
    await patch({notes:'Unrelated requiredness is deferred'});
    for(const [name,v] of Object.entries({text:'One line',textarea:'Multiple\nlines',number:'1.23',date:'2026-01-01',checkbox:'false',select:'Old',multiselect:['Old','Current']}))await patch(value(name,v));
    for(const [name,v] of [['required',''],['text','two\nlines'],['number','1.234'],['date','2026-02-30'],['checkbox','yes'],['select','Missing'],['multiselect',['Current','Current']]])await patch(value(name,v),422);
    assert.equal((await fetch(input.url+'/__fixture/fields?action=retire',{method:'POST'})).status,200);
    let current=await refresh();
    const retained=(doc,name)=>doc.custom_fields.find(f=>f.definition_id===fields[name]).value;
    assert.equal(retained(current,'number'),'1.23');assert.equal(retained(current,'select'),'Old');
    await patch(value('select','Old'));await patch(value('multiselect',['Old','Current']));
    await patch(value('number','2.00'),422);
    await patch(value('select','Current'));await patch(value('multiselect',['Current']));
    await patch(value('select','Old'),422);await patch(value('multiselect',['Old']),422);
    current=await patch({terms:'Retired history retained'});assert.equal(retained(current,'number'),'1.23');
    assert.equal((await fetch(input.url+'/__fixture/fields?action=reactivate',{method:'POST'})).status,200);
    current=await refresh();assert.equal(retained(current,'number'),'1.23','reactivation rewrote history');
    await patch(value('number','2.34'),422);current=await patch(value('number','2.3'));assert.equal(retained(current,'number'),'2.3');
    return;
   }
   if(input.scenario.startsWith('revision_')||input.scenario==='collaboration'){
    assert.equal((await fetch(input.url+'/__fixture/drift',{method:'POST'})).status,204);
    await client.synchronize(input.kind,true);
    const current=(await client.view()).documents[target];
    assert.deepEqual((await client.view()).pending[id].intent,original);
    restart(id,original);
    if(input.scenario==='collaboration'){
     assert.equal(current.revision,snapshot.revision);
     assert.equal((await client.submit(id)).receipt.outcome,'accepted');
     await client.synchronize(input.kind,true);
     assert.equal((await client.view()).documents[target].notes,original.notes);
    }else{
     assert.notEqual(current.revision,snapshot.revision);
     if(input.scenario==='revision_aba')assert.equal(current.notes,snapshot.notes);
     assert.equal((await client.submit(id)).status,409);
     restart(id,null,'locked_review');
     assert.equal((await client.reconcile(id)).receipt.outcome,'stale_commercial_revision');
     const comparison=await client.compare(id);
     assert.deepEqual(comparison.reviewed,snapshot);
     assert.deepEqual(comparison.proposed,original);
     assert.equal(comparison.current.revision,current.revision);
     await client.discard(id);
     await client.synchronize(input.kind);
     assert.equal((await client.view()).documents[target].revision,current.revision,'discard created an effect');
    }
    return;
   }
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
   if(input.scenario==='lifecycle'||input.scenario==='nonzero_lifecycle'){
    await client.discard(id);
    const attribution=async()=>{const r=await fetch(input.url+'/__fixture/attribution');assert.equal(r.status,200);return r.json();};
    const transition=async(category,action='status',selected)=>{
     const doc=(await client.view()).documents[target],choice=selected||doc.workflow.find(w=>w.category===category);
    const operation=await client.stageLifecycle(target,doc.revision,{id:choice.id,meaning_revision:choice.meaning_revision,category:choice.category},action);
    restart(operation,(await client.view()).pending[operation].intent);
    const result=await client.submit(operation);assert.equal(result.receipt?.outcome,'accepted');
    await client.synchronize(input.kind,true);return (await client.view()).documents[target];
   };
    if(input.kind==='invoice'){
     assert(!snapshot.workflow.some(w=>['invoice:paid','invoice:partially_paid'].includes(w.category)));
     if(input.scenario==='nonzero_lifecycle'){
      assert.equal(snapshot.manual_category,'invoice:draft');assert.equal(snapshot.totals.total,'100.00');
      for(const from of ['invoice:invoiced','invoice:sent','invoice:void']){
       for(const to of ['invoice:invoiced','invoice:sent','invoice:void','invoice:draft']){
        if((await client.view()).documents[target].manual_category==='invoice:draft')await transition('invoice:invoiced','finalize');
        await transition(from);const doc=await transition(to);
        assert.equal(doc.manual_category,to);assert.equal(doc.effective_category,to);assert.equal(doc.settlement_locked,false);assert.equal(doc.totals.total,'100.00');
       }
      }
      return;
     }
    let doc=await transition('invoice:invoiced','finalize');assert.equal(doc.effective_category,'invoice:paid');assert.equal(doc.settlement_locked,false);
     const revision=doc.revision,before=await attribution();doc=await transition('invoice:invoiced');assert.equal(doc.revision,revision,'desired-state no-op changed commercial attribution');assert.deepEqual(await attribution(),before);
    doc=await transition('invoice:void');assert.equal(doc.effective_category,'invoice:void');
    doc=await transition('invoice:sent');assert.equal(doc.effective_category,'invoice:paid');
    doc=await transition('invoice:draft');assert.equal(doc.effective_category,'invoice:draft');
   }else{
     for(const choice of snapshot.workflow){
      let doc=await transition(choice.category,'status',choice);assert.equal(doc.manual_category,choice.category);assert.equal(doc.actions.edit,true);
      const revision=doc.revision,before=await attribution();doc=await transition(choice.category,'status',choice);assert.equal(doc.revision,revision);assert.deepEqual(await attribution(),before);
      const edit=await client.stageEdit(target,doc.revision,{notes:'Editable in '+choice.id});
      restart(edit,(await client.view()).pending[edit].intent);
      assert.equal((await client.submit(edit)).receipt.outcome,'accepted');
      await client.synchronize(input.kind,true);doc=(await client.view()).documents[target];
      assert.equal(doc.manual_category,choice.category,'commercial edit automatically accepted Estimate');assert.equal(doc.status_id,choice.id);
     }
   }
   return;
  }
   if(input.scenario==='restore'||input.scenario==='revoke'||input.scenario.startsWith('repeated_restore_')){
   const changed=await fetch(input.url+'/__fixture/change',{method:'POST'});
    await assert.rejects(client.synchronize(input.kind),input.scenario==='revoke'?/authorization_purged/:/locked_review/);
   restart(id,null,'locked_review');
   const persisted=await store.load();assert.deepEqual(persisted.documents,{});
   if(input.scenario==='revoke'){assert(!persisted.pending[id].intent);assert(!persisted.token);return;}
   const fresh=await changed.json();await client.authenticate(fresh.token);await client.synchronize(input.kind);
   assert.deepEqual((await client.view()).pending[id].intent,original);
   await assert.rejects(client.submit(id),/reconciliation_or_review_required/);
    await client.reconcile(id);await client.synchronize(input.kind);await client.acknowledgeUncertain(id);
    if(input.scenario.startsWith('repeated_restore_')){
     const again=await fetch(input.url+'/__fixture/change',{method:'POST'}),token=(await again.json()).token;
     if(input.scenario.endsWith('_contact'))await assert.rejects(client.synchronize(input.kind),{message:'locked_review'});
     await client.authenticate(token);restart(id,null,'locked_review');
     await client.synchronize(input.kind);
     for(let attempt=0;attempt<2;attempt++){
      await client.authenticate(token);await client.synchronize(input.kind);restart(id,original);
      assert.deepEqual((await client.view()).pending[id].intent,original);
      await assert.rejects(client.acknowledgeUncertain(id),{message:'restore_reconciliation_required'});
      await assert.rejects(client.repair(id,(await client.view()).documents[target].revision,{notes:original.notes}),{message:'restore_reconciliation_required'});
     }
     assert.equal(writes,0,'authentication and synchronization must not replay intent');
     assert.equal((await client.reconcile(id)).state,'restore_uncertain');
     await client.synchronize(input.kind);await client.acknowledgeUncertain(id);restart(id,original);
    }
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
