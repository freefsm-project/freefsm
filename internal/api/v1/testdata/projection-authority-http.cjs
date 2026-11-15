'use strict';
const assert=require('node:assert/strict');
const {spawnSync}=require('node:child_process');
const {PaymentClient,ProtectedStore}=require('../reference/payment-client.cjs');
const {OfflineFieldClient}=require('../reference/offline-notes-client.cjs');

module.exports=async({input,store,transport,control,fresh})=>{
 let loseRequest=false;
 const paymentTransport=async(path,method,...args)=>{if(loseRequest&&method==='POST'&&path.endsWith('/payments')){loseRequest=false;throw Error('lost_request');}return transport(path,method,...args);};
 const payment=()=>new PaymentClient(new ProtectedStore(input.directory,store.id),paymentTransport);
 if(input.projectionRestart){
  const client=payment();await client.initialize();
  // This is deliberately BEFORE any Payment synchronization or network request.
  await assert.rejects(client.view(),/locked_review/);
  const state=await store.load();
  assert(!JSON.stringify(state.documents||{}).includes('PRIVATE-PAYMENT'));
  assert.deepEqual(state.documents||{},{});
  return;
 }
 let client=payment();await client.initialize();await client.authenticate(input.token);await client.synchronize();
 const documents=(await client.view()).documents,target=Object.keys(documents)[0];assert(target);
 assert(JSON.stringify(documents).includes('PRIVATE-PAYMENT-INVOICE'));
 const values={amount:'25.00',method:'cash',date:'2026-01-02',note:'PRIVATE-PAYMENT-PROJECTION'};
 const pending=input.scenario.endsWith('_pending');
 const id=pending?await client.stagePayment(target,values):null;
 if(pending){loseRequest=true;await assert.rejects(client.submit(id),/lost_request/);}
 const mode=input.scenario.split('_')[1];
 const notes=new OfflineFieldClient(new ProtectedStore(input.directory,store.id),transport);
 await notes.initialize();
 if(mode==='user')await notes.authenticate(await fresh('notes-user'));
 if(mode==='grant')await control('invoice-loss');
 if(mode==='assignment')await control('scope-loss');
 if(mode==='restore')await notes.authenticate(await fresh('restore'));
 await notes.synchronize();
 const view=await notes.view();
 if(mode==='assignment')assert.deepEqual(view.cache,{});else assert(Object.keys(view.cache).length);
 assert(!JSON.stringify(view).includes('PRIVATE-PAYMENT-PROJECTION'));
 const child=spawnSync(process.execPath,[require.resolve('./payment-protected-http.cjs')],{input:JSON.stringify({...input,key:store.id,projectionRestart:true}),encoding:'utf8',timeout:30000});
 assert.equal(child.status,0,child.stdout+child.stderr);
 client=payment();await client.initialize();
 if(mode==='user'){
  assert.deepEqual((await store.load()).pending,{});
  await assert.rejects(client.synchronize(),/authorization_purged/);
  await client.authenticate(await fresh('new-session'));
 }else if(mode==='grant'){
  await assert.rejects(client.synchronize(),/authorization_purged/);await control('grant-regain');
  await client.authenticate(await fresh('new-session'));
 }else if(mode==='assignment'){
  await client.synchronize();assert.deepEqual((await client.view()).documents,{});await control('scope-regain');
 }
 await client.synchronize();assert(Object.keys((await client.view()).documents).length);
 if(pending&&mode!=='user'){
  const e=(await store.load()).pending[id];assert(e);assert.equal(e.intent,undefined);
  await assert.rejects(client.stagePayment(target,values),/reconciliation_required/);
  await assert.rejects(client.discard(id),/reconciliation_required/);
 }
};
