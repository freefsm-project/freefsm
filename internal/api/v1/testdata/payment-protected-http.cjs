'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const {randomUUID}=require('node:crypto');
const {spawnSync}=require('node:child_process');
const {PaymentClient,ProtectedStore}=require('../reference/payment-client.cjs');
(async()=>{
 const input=JSON.parse(fs.readFileSync(0,'utf8'));
  const store=new ProtectedStore(input.directory,input.key||randomUUID());
  if(input.cleanup){await store.destroy();assert(!fs.existsSync(input.directory+'/cache'));return;}
 let offline=false,loss=null,posts=0,invalid=false;
 const calls=[];
 const transport=async(path,method='GET',body,auth)=>{
  assert(!offline);assert(!/credit|refund|reversal/.test(path));calls.push([method,path]);
  const effect=method==='POST'&&path.endsWith('/payments');
  if(effect){posts++;assert.equal((await store.load()).pending[body.operation_id].state,'unknown');if(loss==='before'){loss=null;throw Error('lost_request');}}
  const headers={Authorization:'Bearer '+auth.token,'Content-Type':'application/json'};
  if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;
  if(auth.operation)headers['Operation-ID']=auth.operation;
  const r=await fetch(input.url+path,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
  const result={status:r.status,...await r.json()};
  if(effect&&loss==='after'){loss=null;assert.equal(r.status,200);throw Error('lost_response');}
  if(invalid&&path.startsWith('/payment-operations/'))result.data={...result.data,operation_id:randomUUID(),note:'PRIVATE-INJECTED'};
  return result;
 };
 const control=async action=>{const r=await fetch(input.url+'/__fixture/'+action,{method:'POST',headers:{Authorization:'Bearer '+input.token}});assert(r.ok,action+': '+await r.text().then(x=>{return r.status===204?x:'';}));return r;};
 // JSON-returning controls need their body intact.
 const fresh=async action=>{const r=await fetch(input.url+'/__fixture/'+action,{method:'POST',headers:{Authorization:'Bearer '+input.token}});assert(r.ok);return (await r.json()).token;};
 let client=new PaymentClient(store,transport);
  if(input.restart){await client.initialize();assert.equal((await client.reconcile(input.id)).receipt.outcome,'accepted');assert.equal(posts,0);return;}
  if(input.sharedRestart){await client.initialize();await client.synchronize();await assert.rejects(client.stagePayment(input.target,input.values),/reconciliation_required/);await assert.rejects(client.discard(input.id),/reconciliation_required/);assert.equal(posts,0);return;}
 try{
  await client.initialize();await client.authenticate(input.token);await client.synchronize();
  const target=Object.keys((await client.view()).documents)[0];assert(target);
   const values={amount:input.scenario.startsWith('shared_')?'25.00':'100.00',method:'cash',date:'2026-01-02',reference:'PRIVATE-PAYMENT-REFERENCE',note:'PRIVATE-PAYMENT-NOTE'};
  offline=true;
  for(const amount of ['0.00','-1.00','NaN','1.001'])await assert.rejects(client.stagePayment(target,{...values,amount}),/review_required/);
  await assert.rejects(client.stagePayment(target,{...values,method:'other'}),/review_required/);
    const id=await client.stagePayment(target,{...values,...(['terminal','restore_terminal_missing'].includes(input.scenario)||input.scenario.startsWith('generation_terminal_')?{date:'2999-01-01'}:{})});offline=false;
  const original=structuredClone((await store.load()).pending[id].intent);
  for(const name of fs.readdirSync(input.directory))for(const secret of ['PRIVATE-PAYMENT',input.token])assert(!fs.readFileSync(input.directory+'/'+name).includes(Buffer.from(secret)));
   if(input.scenario.startsWith('shared_')){
    const adapters={
     commercial:require('../reference/commercial-client.cjs').CommercialClient,
     pdf:require('../reference/pdf-client.cjs').PDFClient,
     delivery:require('../reference/delivery-client.cjs').DeliveryClient,
     notes:require('../reference/offline-notes-client.cjs').OfflineFieldClient,
     clocks:require('../reference/offline-clocks-client.cjs').OfflineClocks,
     creation:require('../reference/document-creation-client.cjs').DocumentCreationClient,
    };
    const family=input.scenario.slice(7),Adapter=adapters[family]||adapters.commercial;
    const reopen=async()=>{client=new PaymentClient(new ProtectedStore(input.directory,store.id),transport);await client.initialize();};
    const other=()=>new Adapter(new ProtectedStore(input.directory,store.id),transport);
    const syncOther=async()=>{const c=other();await c.initialize();await c.synchronize(['notes','clocks','creation'].includes(family)?false:'invoice');return c;};
    const safe=async()=>{const e=(await store.load()).pending[id];assert(e);for(const k of ['intent','reviewed','target','customer'])assert.equal(e[k],undefined,k);assert(!JSON.stringify(e).includes('PRIVATE-'));return e;};
    const blocked=async()=>{await assert.rejects(client.stagePayment(target,values),/reconciliation_required/);await assert.rejects(client.discard(id),/reconciliation_required/);};
    const restored=family.startsWith('restore_')||family==='generation';
    let prior;
    if(restored){
     await client.submit(id);prior=(await client.reconcile(id)).receipt;
     if(family!=='generation'){
      await control('forget-receipt');await client.authenticate(await fresh('restore'));await client.synchronize();
      if(family!=='restore_before_lookup'){await client.reconcile(id);await client.synchronize();}
      if(family==='restore_after_ack')await client.acknowledgeUncertain(id);
     }
    }else{loss=family==='ordinary_missing'?'before':'after';await assert.rejects(client.submit(id),/lost_/);}
    if(family==='purge'){
     await control('session-loss');await assert.rejects(syncOther(),/authorization_purged/);
     await other().authenticate(await fresh('new-session'));
    }else await syncOther();
    if(family==='other_user'){
     await other().authenticate(await fresh('other-user'));await reopen();await client.synchronize();
     assert.equal((await store.load()).pending[id],undefined);await assert.rejects(client.reconcile(id),/unknown_operation/);assert.equal(posts,1);return;
    }
    if(family==='generation'){
     await control('session-loss');await assert.rejects(syncOther(),/authorization_purged/);
     await control('forget-receipt');await other().authenticate(await fresh('restore'));
     assert.equal((await safe()).receipt,undefined,'other adapter must archive old generation before returning');
    }
    await reopen();await client.synchronize();await blocked();
    const preserved=await safe();assert(preserved.generation);assert(preserved.financialUncertain||restored);
    await assert.rejects(other().discard(id),/reconciliation_required/);
    // A separate process must observe the same durable receipt-first boundary.
    const child=spawnSync(process.execPath,[__filename],{input:JSON.stringify({...input,key:store.id,id,target,values,sharedRestart:true}),encoding:'utf8',timeout:30000});assert.equal(child.status,0,child.stdout+child.stderr);
    calls.length=0;const result=await client.reconcile(id);assert.deepEqual(calls[0],['GET','/payment-operations/'+id]);assert.equal(posts,1);
    await reopen();await client.synchronize();await safe();
    if(restored){
     assert.equal(result.state,'restore_uncertain');assert.equal(result.receipt,undefined);assert.deepEqual(result.historicalReceipts,[prior]);
     await client.acknowledgeUncertain(id);
     await other().authenticate(await fresh('restore'));await reopen();await client.synchronize();
     await blocked();await assert.rejects(client.acknowledgeUncertain(id),/restore_reconciliation_required/);
     assert.equal((await client.reconcile(id)).state,'restore_uncertain');await client.synchronize();await client.acknowledgeUncertain(id);
    }else if(family==='ordinary_missing'){
     assert.equal(result.receipt,undefined);await blocked();await assert.rejects(client.acknowledgeUncertain(id),/restore_reconciliation_required/);return;
    }else{assert.equal(result.receipt.outcome,'accepted');assert.deepEqual(Object.keys(result.receipt).sort(),['accepted_at','operation_id','outcome','payment_id']);}
    const next=await client.stagePayment(target,{...values,amount:'1.00'});assert.notEqual(next,id);assert.equal(posts,1);return;
   }
   if(input.scenario==='tampered_store'){
   const raw=fs.readFileSync(input.directory+'/cache');raw[raw.length-1]^=1;fs.writeFileSync(input.directory+'/cache',raw);
   await assert.rejects(client.view(),/protected_store_invalid/);assert.equal(posts,0);return;
  }
  if(input.scenario==='expiry'){client.now=()=>Date.now()+73*3600000;await assert.rejects(client.view(),/authorization_lease_expired/);await assert.rejects(client.submit(id),/authorization_lease_expired/);assert.equal(posts,0);return;}
   if(input.scenario==='revoked'){await control('revoke');await assert.rejects(client.submit(id),/authorization_purged/);assert(!(await store.load()).pending[id].intent);return;}
     if(input.scenario.startsWith('generation_')){
      const restart=async()=>{client=new PaymentClient(new ProtectedStore(input.directory,store.id),transport);await client.initialize();};
      await client.submit(id);const prior=(await client.reconcile(id)).receipt;
      const terminal=input.scenario.startsWith('generation_terminal_');
      assert.equal(prior.outcome,terminal?'invalid_payment':'accepted');
      // Resolve a restore against an existing receipt before ordinary purge.
      await client.authenticate(await fresh('restore'));await client.synchronize();
      assert.deepEqual((await client.reconcile(id)).receipt,prior);await restart();await client.synchronize();
      const grant=input.scenario==='generation_grant_missing';
      if(grant){await control('grant-loss');await assert.rejects(client.synchronize(),/authorization_purged/);}
      else {await control('session-loss');await assert.rejects(client.reconcile(id),/authorization_purged/);}
      await restart();
      const safe=async()=>{const e=(await store.load()).pending[id];for(const field of ['intent','reviewed','target','customer'])assert.equal(e[field],undefined);assert(!JSON.stringify(e).includes('PRIVATE-'));};
      await safe();await assert.rejects(client.stagePayment(target,values),/locked_review/);
      if(grant)await control('grant-regain');
      // Same-generation authentication must not fabricate restore uncertainty.
      await client.authenticate(await fresh('new-session'));await client.synchronize();
      assert.deepEqual((await client.reconcile(id)).receipt,prior);
      await control('session-loss');await assert.rejects(client.reconcile(id),/authorization_purged/);await restart();
      const existing=input.scenario.endsWith('_existing');
      if(!existing)await control('forget-receipt');
      const token=await fresh('restore');
      if(input.scenario==='generation_other_user'){
       await client.authenticate(await fresh('other-user'));await client.synchronize();assert.equal((await store.load()).pending[id],undefined);
       await assert.rejects(client.reconcile(id),/unknown_operation/);assert.equal(posts,1);return;
      }
      await client.authenticate(token);await restart();
      assert.equal((await store.load()).pending[id].receipt,undefined,'old-generation receipt must not remain current after authentication');
      await safe();await assert.rejects(client.acknowledgeUncertain(id),/locked_review/);
      if(!existing){assert.equal((await client.reconcile(id)).receipt,undefined);await restart();}
      await client.synchronize();assert.equal((await client.view()).pending[id].receipt,undefined);
      await assert.rejects(client.stagePayment(target,values),/reconciliation_required/);
      await assert.rejects(client.acknowledgeUncertain(id),/restore_reconciliation_required/);
      calls.length=0;const result=await client.reconcile(id);assert.deepEqual(calls[0],['GET','/payment-operations/'+id]);assert.equal(posts,1);
      assert.deepEqual(result.historicalReceipts,[prior]);await restart();await safe();
      if(existing){assert.deepEqual(result.receipt,prior);assert.equal(result.state,terminal?'terminal':'accepted');return;}
      assert.equal(result.receipt,undefined);assert.equal(result.state,'restore_uncertain');
      await assert.rejects(client.acknowledgeUncertain(id),/locked_review/);await client.synchronize();await client.acknowledgeUncertain(id);await restart();
      // A subsequent epoch invalidates durable lookup and acknowledgment again.
      await client.authenticate(await fresh('restore'));await restart();await client.synchronize();
      await assert.rejects(client.acknowledgeUncertain(id),/restore_reconciliation_required/);await assert.rejects(client.stagePayment(target,values),/reconciliation_required/);
      assert.equal((await client.reconcile(id)).state,'restore_uncertain');await restart();await client.synchronize();await client.acknowledgeUncertain(id);
      const next=await client.stagePayment(target,{...values,amount:'1.00'});assert.notEqual(next,id);assert.equal(posts,1);await safe();
      await assert.rejects(client.discard(id),/reconciliation_required/);return;
     }
     if(input.scenario.startsWith('restore_purge_')||input.scenario==='ordinary_purge_missing'){
     const ordinary=input.scenario==='ordinary_purge_missing';
     let prior;
     if(ordinary){loss='before';await assert.rejects(client.submit(id),/lost_request/);}
     else {await client.submit(id);prior=(await client.reconcile(id)).receipt;await control('forget-receipt');await client.authenticate(await fresh('restore'));await client.synchronize();}
     const restart=async()=>{client=new PaymentClient(new ProtectedStore(input.directory,store.id),transport);await client.initialize();};
     const safe=async()=>{const e=(await store.load()).pending[id];for(const field of ['intent','reviewed','target','customer'])assert.equal(e[field],undefined);if(prior)assert.deepEqual(e.historicalReceipts,[prior]);assert(!JSON.stringify(e).includes('PRIVATE-'));};
     if(input.scenario!=='restore_purge_before_lookup'&&!ordinary){await client.reconcile(id);await restart();await assert.rejects(client.acknowledgeUncertain(id),/locked_review/);await client.synchronize();}
     if(input.scenario==='restore_purge_after_ack')await client.acknowledgeUncertain(id);
     await control('session-loss');await assert.rejects(client.reconcile(id),/authorization_purged/);await restart();await safe();
     await assert.rejects(client.stagePayment(target,values),/locked_review/);
     await assert.rejects(client.discard(id),/reconciliation_required/);
     if(input.scenario==='restore_purge_other_user'){
      await client.authenticate(await fresh('other-user'));await client.synchronize();assert.equal((await store.load()).pending[id],undefined);
      await assert.rejects(client.reconcile(id),/unknown_operation/);assert.equal(posts,1);return;
     }
     await client.authenticate(await fresh(input.scenario==='restore_purge_before_lookup'?'restore':'new-session'));await restart();await assert.rejects(client.acknowledgeUncertain(id),/locked_review/);await client.synchronize();
     await assert.rejects(client.stagePayment(target,values),/reconciliation_required/);
     await assert.rejects(client.acknowledgeUncertain(id),/restore_reconciliation_required/);
     const result=await client.reconcile(id);assert.equal(result.receipt,undefined);await restart();await client.synchronize();
     if(ordinary){assert.equal(result.state,'purged');await assert.rejects(client.acknowledgeUncertain(id),/restore_reconciliation_required/);await assert.rejects(client.stagePayment(target,values),/reconciliation_required/);return;}
     assert.equal(result.state,'restore_uncertain');await safe();
     await client.acknowledgeUncertain(id);await restart();assert.equal(posts,1);
     // A later restore must invalidate even a durable acknowledgment.
     await client.authenticate(await fresh('restore'));await restart();await client.synchronize();
     await assert.rejects(client.acknowledgeUncertain(id),/restore_reconciliation_required/);await assert.rejects(client.stagePayment(target,values),/reconciliation_required/);
     assert.equal((await client.reconcile(id)).state,'restore_uncertain');await restart();await assert.rejects(client.acknowledgeUncertain(id),/locked_review/);
     await client.synchronize();await assert.rejects(client.stagePayment(target,values),/reconciliation_required/);await client.acknowledgeUncertain(id);
     const next=await client.stagePayment(target,{...values,amount:'1.00'});assert.notEqual(next,id);assert.equal(posts,1);await safe();
     await assert.rejects(client.discard(id),/reconciliation_required/);return;
    }
    if(input.scenario.startsWith('restore_completed_')||input.scenario==='restore_terminal_missing'){
    const terminal=input.scenario==='restore_terminal_missing';
    await client.submit(id);const prior=(await client.reconcile(id)).receipt;
    assert.equal(prior.outcome,terminal?'invalid_payment':'accepted');
    if(terminal)await client.authenticate(await fresh('new-session')); // ordinary purge removes private terminal intent
    assert(!(await store.load()).pending[id].intent);
    const existing=input.scenario==='restore_completed_existing';
    if(!existing)await control('forget-receipt');
    if(input.scenario==='restore_completed_scope_loss')await control('scope-loss');
     for(let cycle=0;cycle<(['restore_completed_repeated','restore_completed_grant_loss'].includes(input.scenario)?2:1);cycle++){
      if(input.scenario==='restore_completed_grant_loss')await control('grant-loss');
     const token=await fresh('restore');
     client=new PaymentClient(new ProtectedStore(input.directory,store.id),transport);await client.initialize();
     await client.authenticate(token);await assert.rejects(client.acknowledgeUncertain(id),/locked_review/);
     if(input.scenario==='restore_completed_grant_loss'){
      await assert.rejects(client.synchronize(),/authorization_purged/);
      assert.deepEqual((await store.load()).pending[id].historicalReceipts,[prior]);
      assert(!(await store.load()).pending[id].intent);
      await assert.rejects(client.stagePayment(target,values),/locked_review/);
       await assert.rejects(client.discard(id),/reconciliation_required/);assert.equal(posts,1);
       await control('grant-regain');
       client=new PaymentClient(new ProtectedStore(input.directory,store.id),transport);await client.initialize();
       await client.authenticate(await fresh('new-session'));
     }
     await client.synchronize();
     await assert.rejects(client.acknowledgeUncertain(id),/restore_reconciliation_required/);
     await assert.rejects(client.stagePayment(target,values),/reconciliation_required/);
     const result=await client.reconcile(id);
     assert.equal(posts,1);assert(!(await store.load()).pending[id].intent);
     if(existing){assert.deepEqual(result.receipt,prior);assert.equal(result.state,'accepted');break;}
     assert.equal(result.state,'restore_uncertain');assert.equal(result.receipt,undefined);
     assert.deepEqual(result.historicalReceipts,[prior]);
     assert.deepEqual((await store.load()).pending[id].historicalReceipts,[prior]);
     await assert.rejects(client.acknowledgeUncertain(id),/locked_review/);
     await client.synchronize();await assert.rejects(client.discard(id),/reconciliation_required/);
     await assert.rejects(client.stagePayment(target,values),/reconciliation_required/);
     await client.acknowledgeUncertain(id);assert.equal(posts,1);
    }
    await client.synchronize();
    if(input.scenario==='restore_completed_scope_loss')await assert.rejects(client.stagePayment(target,values),/review_required/);
    else {const next=await client.stagePayment(target,values);assert.notEqual(next,id);assert.equal(posts,1);}
    // Staging never submits, and retained uncertainty is never discarded.
    if(!existing)await assert.rejects(client.discard(id),/reconciliation_required/);
    if(terminal){const next=Object.keys((await store.load()).pending).find(k=>k!==id);assert.equal((await client.submit(next)).receipt.outcome,'accepted');}
    return;
   }
  if(input.scenario==='restore'){
   const token=await fresh('restore');await assert.rejects(client.submit(id),/locked_review/);
   client=new PaymentClient(new ProtectedStore(input.directory,store.id),transport);
   await client.authenticate(token);await client.synchronize();
   await assert.rejects(client.repairPayment(id,values),/restore_reconciliation_required/);
   assert.equal((await client.reconcile(id)).state,'restore_uncertain');await client.synchronize();
   await client.acknowledgeUncertain(id);const next=await client.repairPayment(id,values);assert.notEqual(next,id);assert.equal(posts,0);
   assert.equal((await client.submit(next)).receipt.outcome,'accepted');return;
  }
  if(input.scenario==='terminal'){
   assert.equal((await client.submit(id)).status,422);assert.equal((await client.reconcile(id)).receipt.outcome,'invalid_payment');await client.synchronize();
   const next=await client.repairPayment(id,values);assert.notEqual(next,id);assert.deepEqual((await store.load()).pending[id].intent,original);
   assert.equal((await client.submit(next)).receipt.outcome,'accepted');return;
  }
  if(input.scenario==='transient'){
   await control('fail');assert.equal((await client.submit(id)).status,503);await control('recover');
   assert.equal((await client.reconcile(id)).state,'unreceived');await assert.rejects(client.retryTransient(id),/review_required/);
   client.now=()=>Date.now()+6000;
   assert.equal((await client.retryTransient(id,{confirm:true})).receipt.outcome,'accepted');assert.equal(posts,2);return;
  }
  if(input.scenario==='rename'){await control('rename');await client.synchronize();assert.deepEqual((await store.load()).pending[id].intent,original);}
  loss=input.scenario==='absent'?'before':'after';await assert.rejects(client.submit(id),/lost_/);
  client=new PaymentClient(new ProtectedStore(input.directory,store.id),transport);await client.initialize();
  assert.deepEqual((await store.load()).pending[id].intent,original);
  if(input.scenario==='scope_loss')await control('scope-loss');
  if(input.scenario==='grant_loss')await control('grant-loss');
  if(input.scenario==='new_session')await client.authenticate(await fresh('new-session'));
  if(input.scenario==='invalid_receipt'){invalid=true;await assert.rejects(client.reconcile(id),/receipt_mismatch/);assert(!(await store.load()).pending[id].receipt);await assert.rejects(client.view(),/locked_review/);invalid=false;}
  if(input.scenario==='lost'){
   const child=spawnSync(process.execPath,[__filename],{input:JSON.stringify({...input,key:store.id,id,restart:true}),encoding:'utf8',timeout:30000});assert.equal(child.status,0,child.stdout+child.stderr);
  }
  calls.length=0;const result=await client.reconcile(id);assert.deepEqual(calls[0],['GET','/payment-operations/'+id]);assert.equal(posts,1);
  if(input.scenario==='absent'){
   assert.equal(result.state,'unreceived');await client.synchronize();
   await assert.rejects(client.submit(id),/reconciliation_or_review_required/);await assert.rejects(client.retryTransient(id,{confirm:true}),/review_required/);
   await client.synchronize();await assert.rejects(client.stagePayment(target,values),/reconciliation_required/);await assert.rejects(client.discard(id),/reconciliation_required/);assert.equal(posts,1);
  }else{
   assert.equal(result.receipt.outcome,'accepted');assert.deepEqual(Object.keys(result.receipt).sort(),['accepted_at','operation_id','outcome','payment_id']);
   assert(!(await store.load()).pending[id].intent);await assert.rejects(client.view(),/locked_review/);
   if(input.scenario==='scope_loss'){await client.synchronize();assert.deepEqual((await client.view()).documents,{});}
   if(input.scenario==='grant_loss'){await assert.rejects(client.synchronize(),/authorization_purged/);assert(!(await store.load()).pending[id].intent);}
  }
 }finally{if(!input.key){await client.logout();assert(!fs.existsSync(input.directory+'/cache'));}}
 console.log('PASS protected Payment '+input.scenario);
})().catch(e=>{console.error(e);process.exit(1);});
