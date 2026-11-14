'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const {PaymentClient,ProtectedStore}=require('../reference/payment-client.cjs');
(async()=>{
 const input=JSON.parse(fs.readFileSync(0,'utf8'));
 const store=new ProtectedStore(input.directory,input.key);let posts=0;
 const transport=async(path,method='GET',body,auth)=>{
  assert(!/credit|refund|reversal/.test(path));
  if(method==='POST'&&path.endsWith('/payments'))posts++;
  const headers={Authorization:'Bearer '+auth.token,'Content-Type':'application/json'};
  if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;
  if(auth.operation)headers['Operation-ID']=auth.operation;
  const r=await fetch(input.url+path,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
  return {status:r.status,...await r.json()};
 };
 const client=new PaymentClient(store,transport);await client.initialize();
 const values={amount:'25.00',method:'cash',date:'2026-01-02',note:'PRIVATE-RESTORE-PAYMENT'};
 if(input.phase==='accept'){
  await client.authenticate(input.token);await client.synchronize();
  const target=Object.keys((await client.view()).documents)[0];
  const id=await client.stagePayment(target,values);
  assert.equal((await client.submit(id)).receipt.outcome,'accepted');
  assert(!(await store.load()).pending[id].intent);assert.equal(posts,1);
  for(const name of fs.readdirSync(input.directory))assert(!fs.readFileSync(input.directory+'/'+name).includes(Buffer.from(values.note)));
  console.log(id);return;
 }
 const id=Object.keys((await store.load()).pending)[0],prior=(await store.load()).pending[id].receipt;
 assert.equal(prior.outcome,'accepted');
 await client.authenticate(input.token);await assert.rejects(client.view(),/locked_review/);
 await client.synchronize();const target=Object.keys((await client.view()).documents)[0];
 await assert.rejects(client.stagePayment(target,values),/reconciliation_required/);
 const result=await client.reconcile(id);
 assert.equal(result.state,'restore_uncertain');assert.equal(result.receipt,undefined);
 assert.deepEqual((await store.load()).pending[id].historicalReceipts,[prior]);
 assert(!(await store.load()).pending[id].intent);assert.equal(posts,0);
 await client.synchronize();await assert.rejects(client.stagePayment(target,values),/reconciliation_required/);
 await assert.rejects(client.discard(id),/reconciliation_required/);
 await client.acknowledgeUncertain(id);assert.equal(posts,0);
 const next=await client.stagePayment(target,values);assert.notEqual(next,id);assert.equal(posts,0);
 assert.equal((await client.submit(next)).receipt.outcome,'accepted');assert.equal(posts,1);
 assert.deepEqual((await store.load()).pending[id].historicalReceipts,[prior]);
 await client.logout();
 console.log('PASS actual backup completed Payment recovery');
})().catch(e=>{console.error(e);process.exit(1);});
