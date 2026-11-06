'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const {randomUUID}=require('node:crypto');
const {spawnSync}=require('node:child_process');
const {CommercialClient,ProtectedStore}=require('../reference/commercial-client.cjs');
(async()=>{
 const input=JSON.parse(fs.readFileSync(0,'utf8')),key=input.key||randomUUID();
 const store=new ProtectedStore(input.directory,key);
 let interrupt=false,lose=false,posts=0;
 const transport=async(path,method='GET',body,auth)=>{
  const headers={Authorization:'Bearer '+auth.token};
  if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;
  if(auth.operation)headers['Operation-ID']=auth.operation;
  if(body!==undefined)headers['Content-Type']='application/json';
  if(path==='/sync/operations'){
   posts++;
   const persisted=await store.load();
   for(const op of body.operations)assert.equal(persisted.pending[op.operation_id].state,'unknown','group not durably unknown before transport');
   if(interrupt){headers['X-Batch-Interrupt']='yes';interrupt=false;}
  }
  const r=await fetch(input.url+path,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
  const result={status:r.status,...await r.json()};
  if(lose&&path==='/sync/operations'){lose=false;throw Error('lost_response');}
  return result;
 };
 const client=new CommercialClient(store,transport);
 if(input.worker){
  let result;
  if(input.worker==='reconcile')result=await client.reconcile(input.id);
   else if(input.worker==='locked')await assert.rejects(client.view(),{message:input.lock});
  else result=await client.view();
  process.stdout.write(JSON.stringify(result||{}));return;
 }
 const restart=(worker='view',extra={})=>{
  const r=spawnSync(process.execPath,[__filename],{input:JSON.stringify({...input,key,worker,...extra}),encoding:'utf8',timeout:30000});
  assert.equal(r.status,0,r.stdout+r.stderr);return JSON.parse(r.stdout);
 };
 const fixture=async(path,body)=>fetch(input.url+'/__fixture/'+path,{method:'POST',body:body&&JSON.stringify(body)});
 const sync=()=>client.synchronize(input.kind);
 try{
  await client.initialize();await client.authenticate(input.token);await sync();
  const docs=Object.values((await client.view()).documents);
  assert.equal(docs.length,3);
  const stage=(n,notes,deps=[])=>client.stageBatch(docs[n].id,docs[n].revision,{notes},'edit',deps);
  const a=await stage(0,'ENCRYPTED-BATCH-FIRST');
  const deps=[a,a],values={notes:'ENCRYPTED-BATCH-SECOND'};
  const b=await client.stageBatch(docs[1].id,docs[1].revision,values,'edit',deps);
  const c=await stage(2,'ENCRYPTED-BATCH-UNRELATED');
  deps.push(randomUUID());values.notes='caller mutation';
  const original=(await client.view()).pending;
  assert.deepEqual(original[b].batch.depends_on,[a,a]);
  assert.deepEqual(restart().pending,original);
  for(const name of fs.readdirSync(input.directory))assert(!fs.readFileSync(input.directory+'/'+name).includes(Buffer.from('ENCRYPTED-BATCH')));
  if(input.scenario==='batch_success'){
   const results=await client.submitBatch([a,b,c]);
   assert.deepEqual(results.map(r=>r.receipt.outcome),['accepted','accepted','accepted']);
   await sync();const current=(await client.view()).documents;
   assert.equal(current[docs[0].id].notes,'ENCRYPTED-BATCH-FIRST');
   assert.equal(current[docs[1].id].notes,'ENCRYPTED-BATCH-SECOND');
   assert.equal(current[docs[2].id].notes,'ENCRYPTED-BATCH-UNRELATED');
   const order=[b,a,b];
   const next=await client.stageBatch(docs[0].id,current[docs[0].id].revision,{notes:'Ordered meanings'},'edit',order);
   order.reverse();order.pop();
   assert.deepEqual(restart().pending[next].batch.depends_on,[b,a,b]);
   assert.equal((await client.submitBatch([next]))[0].receipt.outcome,'accepted');return;
  }
  if(input.scenario==='batch_recovery'){
   // Original transport identity is a staging decision, never a retry choice.
   await assert.rejects(client.submit(a),/transport_identity_mismatch/);
   const standalone=await client.stageEdit(docs[2].id,docs[2].revision,{terms:'standalone'});
   await assert.rejects(client.submitBatch([standalone]),/reconciliation_or_review_required/);
   await client.discard(standalone);
    interrupt=true;await assert.rejects(client.submitBatch([a,b]),/fetch failed/);
    const interrupted=await store.load();
    for(const id of [a,b]){assert.equal(interrupted.pending[id].state,'unknown');assert.deepEqual(interrupted.pending[id].intent,original[id].intent);assert.deepEqual(interrupted.pending[id].batch,original[id].batch);}
    const sent=posts;
    await assert.rejects(client.submitBatch([a,b]),{message:'locked_review'});assert.equal(posts,sent);
   restart('locked',{lock:'locked_review'});
   assert.deepEqual((await store.load()).pending[b].intent,original[b].intent);
   assert.equal(restart('reconcile',{id:a}).receipt.outcome,'accepted');
   await sync();
   await assert.rejects(client.submitBatch([c]),/reconciliation_required/);
   await assert.rejects(stage(2,'new effect'),/reconciliation_required/);
   assert.equal(restart('reconcile',{id:b}).state,'pending');
   await sync();assert.deepEqual((await client.view()).pending[b].intent,original[b].intent);
   const result=await client.submitBatch([b,c]);assert(result.every(r=>r.receipt.outcome==='accepted'));
   const wire={operation_id:b,...original[b].batch,commercial:{...original[b].intent}};delete wire.commercial.operation_id;
   // Direct production replay probes use the same captured immutable envelope.
   const raw=async body=>{const r=await fetch(input.url+'/sync/operations',{method:'POST',headers:{Authorization:'Bearer '+input.token,'Content-Type':'application/json'},body:JSON.stringify({operations:[body]})});assert.equal(r.status,200);return (await r.json()).data.results[0];};
   assert.equal((await raw(wire)).receipt.outcome,'accepted');
   assert.equal((await raw({...wire,depends_on:[a]})).error.code,'operation_intent_changed');
   assert.equal((await raw({...wire,commercial:{...wire.commercial,notes:'changed'}})).error.code,'operation_intent_changed');return;
  }
  if(input.scenario==='batch_transient'){
   assert.equal((await fixture('fault',{ID:a})).status,204);
   const result=await client.submitBatch([a,b,c]);
   assert.equal(result[0].state,'unknown');assert.equal(result[1].receipt.outcome,'failed_dependency');assert.equal(result[2].receipt.outcome,'accepted');
   restart('locked',{lock:'locked_review'});
   assert.equal(restart('reconcile',{id:a}).state,'pending');
   assert.equal((await fixture('clear')).status,204);
   await sync();client.now=()=>Date.now()+6000;
   assert.equal((await client.submitBatch([a]))[0].receipt.outcome,'accepted');
   assert.equal((await client.reconcile(b)).receipt.outcome,'failed_dependency');await sync();
   await assert.rejects(client.submitBatch([b]),/reconciliation_or_review_required/);
   await assert.rejects(client.repair(b,docs[1].revision,{notes:'repair'}),/transport_identity_mismatch/);
   const comparison=await client.compare(b);
   const next=await client.repairBatch(b,comparison.current.revision,{notes:'Reviewed repair'},[a,a]);
   assert.notEqual(next,b);assert.equal((await client.submitBatch([next]))[0].receipt.outcome,'accepted');
   await client.discard(b);assert(!(await store.load()).pending[b]);return;
  }
   if(['batch_scope_loss','batch_restore','batch_revoke'].includes(input.scenario)||input.scenario.startsWith('batch_repeated_restore_')){
   lose=true;await assert.rejects(client.submitBatch([a,b]),/lost_response/);
   const changed=await fixture('change');
   if(input.scenario==='batch_scope_loss'){
    assert.equal(restart('reconcile',{id:a}).receipt.outcome,'accepted');
    assert.equal(restart('reconcile',{id:b}).receipt.outcome,'accepted');
    await assert.rejects(sync(),/authorization_purged/);
    const state=await store.load();assert.deepEqual(state.documents,{});assert(!state.pending[c].intent);assert(!state.pending[c].reviewed);return;
   }
   if(input.scenario==='batch_revoke'){
    await assert.rejects(client.reconcile(a),/authorization_purged/);
    restart('locked',{lock:'locked_review'});const state=await store.load();
    assert(!state.token);assert.deepEqual(state.documents,{});assert(Object.values(state.pending).every(e=>!e.intent&&!e.reviewed&&!e.batch));return;
   }
   await assert.rejects(client.reconcile(a),/locked_review/);restart('locked',{lock:'locked_review'});
   await client.authenticate((await changed.json()).token);await sync();
   assert.deepEqual((await client.view()).pending[c].intent,original[c].intent);
   await assert.rejects(client.submitBatch([c]),/reconciliation_required/);
    for(const id of [a,b,c]){
     await client.reconcile(id);await sync();
     if((await client.view()).pending[id].state==='restore_uncertain')await client.acknowledgeUncertain(id);
    }
    if(input.scenario.startsWith('batch_repeated_restore_')){
     const token=(await (await fixture('change')).json()).token,sent=posts;
     if(input.scenario.endsWith('_contact'))await assert.rejects(sync(),{message:'locked_review'});
     await client.authenticate(token);restart('locked',{lock:'locked_review'});await sync();
     for(let attempt=0;attempt<2;attempt++){
      await client.authenticate(token);await sync();
      assert.deepEqual(restart().pending[c].intent,original[c].intent);
      assert.deepEqual(restart().pending[c].batch,original[c].batch);
      await assert.rejects(client.acknowledgeUncertain(c),{message:'restore_reconciliation_required'});
      await assert.rejects(client.repairBatch(c,docs[2].revision,{notes:'Explicit restored review'},[]),{message:'restore_reconciliation_required'});
     }
     assert.equal(posts,sent,'authentication and synchronization must not replay batch');
     // Receipts are deployment/client-bound: the new history cannot inherit
     // the old client's accepted receipt as current reconciliation evidence.
     for(const id of [a,b,c]){
      assert.equal(restart('reconcile',{id}).state,'restore_uncertain');await sync();await client.acknowledgeUncertain(id);
     }
     assert.deepEqual(restart().pending[c].intent,original[c].intent);
    }
   const next=await client.repairBatch(c,docs[2].revision,{notes:'Explicit restored review'},[]);
   assert.notEqual(next,c);assert.equal((await client.submitBatch([next]))[0].receipt.outcome,'accepted');return;
  }
  if(input.scenario==='batch_lifecycle'){
   for(const id of [a,b,c])await client.discard(id);
   for(const action of input.kind==='invoice'?['finalize','status']:['status']){
    await sync();const current=(await client.view()).documents[docs[0].id];
    const category=input.kind==='invoice'?(action==='finalize'?'invoice:invoiced':'invoice:void'):'estimate:accepted';
    const choice=current.workflow.find(w=>w.category===category);assert(choice,category);
    const id=await client.stageBatch(current.id,current.revision,{id:choice.id,meaning_revision:choice.meaning_revision,category:choice.category},action,[]);
    assert.deepEqual(restart().pending[id],(await client.view()).pending[id]);
    assert.equal((await client.submitBatch([id]))[0].receipt.outcome,'accepted');
   }return;
  }
  if(input.scenario==='batch_storage'){
   const save=store.save.bind(store);
   store.save=async s=>{if(s.pending[a]?.state==='unknown')throw Error('injected storage failure');return save(s);};
   await assert.rejects(client.submitBatch([a,b]),/protected_store_locked/);
    assert.equal(posts,0);assert.equal(store.quarantined,true);
    assert.deepEqual((await store.load()).pending,original,'failed checkpoint must not rewrite durable original intent');
    assert(fs.existsSync(input.directory+'/.writer'));
    restart('locked',{lock:'store_busy'});return;
  }
  if(['batch_purge_storage','batch_restore_storage'].includes(input.scenario)){
   lose=true;await assert.rejects(client.submitBatch([a,b]),/lost_response/);
   await fixture('change');
   const save=store.save.bind(store);
    store.save=async s=>{if(!s.token)throw Error('injected authority write failure');return save(s);};
    const sent=posts;
    await assert.rejects(client.reconcile(a),/protected_store_locked/);
    assert.equal(store.quarantined,true);assert(fs.existsSync(input.directory+'/.writer'));
    assert.equal(posts,sent,'authority failure must not resubmit batch');
    await assert.rejects(client.view(),{message:'store_busy'});
    const retained=await store.load();
    for(const id of [a,b]){assert.equal(retained.pending[id].state,'unknown');assert.deepEqual(retained.pending[id].intent,original[id].intent);assert.deepEqual(retained.pending[id].batch,original[id].batch);}
    restart('locked',{lock:'store_busy'});return;
  }
  throw Error('unknown scenario');
 }finally{await store.destroy();}
})().catch(e=>{console.error(e);process.exitCode=1;});
