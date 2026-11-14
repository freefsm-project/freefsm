'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const {randomUUID}=require('node:crypto');
const {spawnSync}=require('node:child_process');
const {PaymentClient,ProtectedStore}=require('../reference/payment-client.cjs');
(async()=>{
 const input=JSON.parse(fs.readFileSync(0,'utf8'));
 const store=new ProtectedStore(input.directory,input.key||randomUUID());
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
 try{
  await client.initialize();await client.authenticate(input.token);await client.synchronize();
  const target=Object.keys((await client.view()).documents)[0];assert(target);
  const values={amount:'100.00',method:'cash',date:'2026-01-02',reference:'PRIVATE-PAYMENT-REFERENCE',note:'PRIVATE-PAYMENT-NOTE'};
  offline=true;
  for(const amount of ['0.00','-1.00','NaN','1.001'])await assert.rejects(client.stagePayment(target,{...values,amount}),/review_required/);
  await assert.rejects(client.stagePayment(target,{...values,method:'other'}),/review_required/);
  const id=await client.stagePayment(target,{...values,...(input.scenario==='terminal'?{date:'2999-01-01'}:{})});offline=false;
  const original=structuredClone((await store.load()).pending[id].intent);
  for(const name of fs.readdirSync(input.directory))for(const secret of ['PRIVATE-PAYMENT',input.token])assert(!fs.readFileSync(input.directory+'/'+name).includes(Buffer.from(secret)));
  if(input.scenario==='tampered_store'){
   const raw=fs.readFileSync(input.directory+'/cache');raw[raw.length-1]^=1;fs.writeFileSync(input.directory+'/cache',raw);
   await assert.rejects(client.view(),/protected_store_invalid/);assert.equal(posts,0);return;
  }
  if(input.scenario==='expiry'){client.now=()=>Date.now()+73*3600000;await assert.rejects(client.view(),/authorization_lease_expired/);await assert.rejects(client.submit(id),/authorization_lease_expired/);assert.equal(posts,0);return;}
  if(input.scenario==='revoked'){await control('revoke');await assert.rejects(client.submit(id),/authorization_purged/);assert(!(await store.load()).pending[id].intent);return;}
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
 }finally{await client.logout();assert(!fs.existsSync(input.directory+'/cache'));}
 console.log('PASS protected Payment '+input.scenario);
})().catch(e=>{console.error(e);process.exit(1);});
