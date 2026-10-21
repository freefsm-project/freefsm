'use strict';
const assert=require('node:assert/strict');
const {PaymentReference}=require('./payment-reference-harness.cjs');
function withheld(view,id) {
 assert.equal(view.operation_id,id);
 for(const field of ['job_id','amount','method','date','reference','note','other_description','occurred_at'])assert.equal(view[field],undefined,`retained projection exposed ${field}`);
 assert.deepEqual(JSON.parse(JSON.stringify(view)),{operation_id:id});
}
// Hold delivery only after the production API has authorized and returned bytes.
// No timers or fabricated successful authority responses establish this ordering.
function holdResponse(client, suffix) {
 const original=client.request;
 let arrive,release,armed=true;
 const reached=new Promise(resolve=>{arrive=resolve;});
 const barrier=new Promise(resolve=>{release=resolve;});
 client.request=async(...args)=>{
  const hold=armed&&args[1].endsWith(suffix);if(hold)armed=false;
  const response=await original(...args);
  if(hold){arrive(response);await barrier;}
  return response;
 };
 return {reached,release,reset:()=>{client.request=original;}};
}
function locked(entry,view) {
 assert.equal(entry.state,'locked');assert.equal(entry.intent,undefined);assert.equal(entry.invoice,undefined);
 withheld(view,entry.operation_id);
 const serialized=JSON.parse(JSON.stringify(entry));
 assert.equal(serialized.intent,undefined);assert.equal(serialized.invoice,undefined);
}
(async()=>{
 const [base,token,invoice,job]=process.argv.slice(2);let posts=0,lose=true;
 const request=async(method,path,body)=>{
  if(method==='POST'&&path.endsWith('/payments'))posts++;
  const response=await fetch(base+path,{method,headers:{Authorization:'Bearer '+token,'Content-Type':'application/json'},body:body?JSON.stringify(body):undefined});
  const result={status:response.status,body:await response.json()};
  if(method==='POST'&&path.endsWith('/payments')&&lose){lose=false;throw new Error('response lost after commit');}return result;
 };
 const methods=await request('GET',`/invoices/${invoice}/payment-methods`);
 assert.equal(methods.status,200);
 assert.deepEqual(methods.body.data.map(x=>x.id),['cash','check','credit_card','transfer','other']);
 assert.equal(methods.body.data[4].requires_description,true);
 const client=new PaymentReference(request);
 const entry=client.stage(Number(invoice),Number(job),{amount:'25.00',method:'credit_card',date:'2026-01-02',reference:'fixed-reference'});
 const held=entry.intent;assert.throws(()=>{held.amount='99.00';});
 await client.submit(entry);assert.equal(entry.state,'unknown');assert.equal(posts,1);
 assert.throws(()=>client.stage(Number(invoice),Number(job),{amount:'1.00'}));
 await assert.rejects(client.submit(entry));
 const realRequest=client.request;
 client.request=async(...args)=>{if(args[1].endsWith('/payment-methods'))throw new Error('current authority response unavailable');return realRequest(...args);};
 await client.inspect(entry).catch(()=>{});assert.equal(entry.state,'locked');assert.equal(entry.intent,undefined);
 withheld(held,entry.operation_id);
 client.request=realRequest;await client.inspect(entry);assert.equal(entry.state,'accepted');assert.equal(entry.intent.amount,'25.00');
 client.request=async(...args)=>{const r=await realRequest(...args);r.body.data.outcome='future_outcome';return r;};
 await client.inspect(entry);assert.equal(entry.state,'locked');assert.throws(()=>client.stage(Number(invoice),Number(job),{amount:'1.00'}));
 client.request=async(...args)=>{const r=await realRequest(...args);delete r.body.data.payment_id;return r;};
 await client.inspect(entry);assert.equal(entry.state,'locked');
 client.request=realRequest;
 await client.inspect(entry);assert.equal(entry.state,'accepted');assert.equal(posts,1);
 assert.equal(entry.intent.amount,'25.00');assert.equal(entry.invoice,Number(invoice));
 const replay=await request('POST',`/invoices/${invoice}/payments`,entry.intent);assert.deepEqual(replay.body.data,entry.receipt);
 const changed=await request('POST',`/invoices/${invoice}/payments`,{...entry.intent,amount:'24.00'});assert.equal(changed.status,409);
  const pending=client.stage(Number(invoice),Number(job),{amount:'10.00',method:'cash',date:'2026-01-02',reference:'restore reference',note:'restore note',other_description:'original description',occurred_at:'2026-01-02T12:00:00Z'});
  const restoreView=pending.intent;
  const originalIntent={...restoreView},postsBeforeRestore=posts;
  const oldAuthority=holdResponse(client,'/payment-methods');
  const oldInspection=client.inspect(pending);
  assert.equal((await oldAuthority.reached).status,200);
  client.restoreDetected();assert.equal(pending.intent,undefined);assert.equal(pending.invoice,undefined);await assert.rejects(client.submit(pending));
  withheld(restoreView,pending.operation_id);
  oldAuthority.release();await oldInspection;oldAuthority.reset();
  locked(pending,restoreView);assert.equal(posts,postsBeforeRestore);
  // Ordinary inspection is not an explicit fresh-authentication recovery.
  await client.inspect(pending);locked(pending,restoreView);
  const recoveryAuthority=holdResponse(client,'/payment-methods');
  const oldRecovery=client.reconcileAfterFreshAuthentication(pending);
  assert.equal((await recoveryAuthority.reached).status,200);
  client.restoreDetected();recoveryAuthority.release();await oldRecovery;recoveryAuthority.reset();
  locked(pending,restoreView);await assert.rejects(client.submit(pending));
  assert.throws(()=>client.stage(Number(invoice),Number(job),{amount:'1.00'}));
  await client.reconcileAfterFreshAuthentication(pending);assert.equal(pending.state,'unknown');
  assert.deepEqual({...restoreView},originalIntent);assert.equal(posts,postsBeforeRestore);
 assert.equal(pending.intent.amount,'10.00');assert.equal(pending.invoice,Number(invoice));
 assert.equal(restoreView.amount,'10.00');
 await assert.rejects(client.submit(pending));assert.throws(()=>client.stage(Number(invoice),Number(job),{amount:'10.00'}));
  client.confirmUnreceivedAfterReview(pending);assert.equal(pending.state,'discarded');
  withheld(restoreView,pending.operation_id);
  // Purge wins over stale authority success and stale receipt completion alike.
  for(const suffix of ['/payment-methods','/payment-operations/']) {
   const logoutRace=new PaymentReference(request);
   const logoutEntry=logoutRace.stage(Number(invoice),Number(job),{amount:'1.00',note:'held logout'});
   const logoutHeld=logoutEntry.intent;
   const heldResponse=holdResponse(logoutRace,suffix==='/payment-operations/'?`/payment-operations/${logoutEntry.operation_id}`:suffix);
   const inspection=logoutRace.inspect(logoutEntry);await heldResponse.reached;
   logoutRace.logout();heldResponse.release();await inspection;
   assert.equal(logoutEntry.state,'purged');assert.equal(logoutEntry.intent,undefined);withheld(logoutHeld,logoutEntry.operation_id);
   assert.throws(()=>logoutRace.stage(Number(invoice),Number(job),{amount:'1.00'}));
  }
  const denialRace=new PaymentReference(request);
  const denialEntry=denialRace.stage(Number(invoice),Number(job),{amount:'1.00',note:'held denial'});
  const denialHeld=denialEntry.intent,denialBarrier=holdResponse(denialRace,'/payment-methods');
  const staleSuccess=denialRace.inspect(denialEntry);assert.equal((await denialBarrier.reached).status,200);
  assert.equal((await request('POST','/fixture/unassign',{})).status,200);
  await denialRace.inspect(denialEntry);assert.equal(denialEntry.state,'purged');
  denialBarrier.release();await staleSuccess;denialBarrier.reset();
  assert.equal(denialEntry.state,'purged');assert.equal(denialEntry.intent,undefined);withheld(denialHeld,denialEntry.operation_id);
  assert.equal((await request('POST','/fixture/reassign',{})).status,200);
  assert.equal(posts,postsBeforeRestore);
  console.log('Held real-API authority across restore/re-restore and stale logout/denial completions passed without POST.');
 const denied=client.stage(Number(invoice),Number(job),{amount:'10.00',method:'cash',date:'2026-01-02',reference:'private scope reference',note:'private scope note'});
 const deniedID=denied.intent.operation_id;
 const deniedView=denied.intent;
 assert.equal((await request('POST','/fixture/unassign',{})).status,200);
 await client.submit(denied);
 assert.equal(denied.intent,undefined);assert.equal(denied.invoice,undefined);assert.equal(denied.operation_id,deniedID);
 withheld(deniedView,deniedID);withheld(held,entry.operation_id);
 assert.equal(entry.intent,undefined);assert.ok(!JSON.stringify(client.pending).includes('private scope'));
 assert.equal(denied.receipt.outcome,'not_found');
 await client.inspect(entry);assert.equal(entry.receipt.outcome,'accepted');assert.equal(entry.intent,undefined);
 assert.equal((await request('POST','/fixture/reassign',{})).status,200);
 const grantClient=new PaymentReference(request);
 const forbidden=grantClient.stage(Number(invoice),Number(job),{amount:'10.00',method:'cash',date:'2026-01-02',note:'private grant input'});
 const forbiddenView=forbidden.intent;
 assert.equal((await request('POST','/fixture/remove-payment-grant',{})).status,200);
 await grantClient.submit(forbidden);assert.equal(forbidden.intent,undefined);assert.equal(forbidden.invoice,undefined);assert.equal(forbidden.receipt.outcome,'forbidden');
 withheld(forbiddenView,forbidden.operation_id);
 assert.equal((await request('POST','/fixture/restore-payment-grant',{})).status,200);
 const authClient=new PaymentReference(request);
 const revoked=authClient.stage(Number(invoice),Number(job),{amount:'10.00',method:'cash',date:'2026-01-02',note:'private revoked input'});
 const revokedID=revoked.intent.operation_id;
 const revokedView=revoked.intent;
 assert.equal((await request('POST','/fixture/revoke',{})).status,200);
 await authClient.submit(revoked);
 assert.equal(revoked.intent,undefined);assert.equal(revoked.invoice,undefined);assert.equal(revoked.operation_id,revokedID);
 withheld(revokedView,revokedID);
 assert.ok(!JSON.stringify(authClient.pending).includes('private revoked input'));
 const logoutClient=new PaymentReference(request);const loggedOut=logoutClient.stage(Number(invoice),Number(job),{amount:'1.00',note:'private logout input'});
 const logoutView=loggedOut.intent;
 logoutClient.logout();assert.equal(loggedOut.intent,undefined);assert.equal(loggedOut.invoice,undefined);
 withheld(logoutView,loggedOut.operation_id);
 assert.equal(posts,6);console.log('Fixed intent, lost response, restore review and real scope/grant/session-denial purge conformance passed.');
})().catch(e=>{console.error(e);process.exit(1)});
