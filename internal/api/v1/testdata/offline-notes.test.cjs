'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { mkdtempSync, readFileSync, writeFileSync, chmodSync, existsSync, rmSync } = require('node:fs');
const { tmpdir } = require('node:os');
const { join } = require('node:path');
const { randomUUID, randomBytes } = require('node:crypto');
const { spawnSync } = require('node:child_process');
const { ProtectedStore, OfflineNotes } = require('../reference/offline-notes.cjs');

test('real Secret Service: encrypted restart, wrong key, destruction, unavailable', async () => {
 const dir=mkdtempSync(join(tmpdir(),'offline-')), id=randomUUID();
 const store=new ProtectedStore(dir,id);
 let destroyed=false;
 try {
  await store.create(); await store.save({token:'secret-bearer',pending:'private-notes'});
  assert(!readFileSync(join(dir,'cache')).includes('private-notes'));
  assert.deepEqual(await new ProtectedStore(dir,id).load(),{token:'secret-bearer',pending:'private-notes'});
  const restart=spawnSync(process.execPath,['-e',`const {ProtectedStore}=require(process.argv[1]); new ProtectedStore(process.argv[2],process.argv[3]).load().then(x=>{if(x.pending!=='private-notes')process.exit(2)}).catch(()=>process.exit(3));`,require.resolve('../reference/offline-notes.cjs'),dir,id],{timeout:15000});
  assert.equal(restart.status,0,'fresh process cannot recover protected data');
  await store.exclusive(async()=>{await assert.rejects(new ProtectedStore(dir,id).exclusive(async()=>{}),/store_busy/);});
  const unavailable=spawnSync(process.execPath,['-e',`const {ProtectedStore}=require(process.argv[1]); new ProtectedStore(process.argv[2],process.argv[3]).load().then(()=>process.exit(2)).catch(e=>process.exit(e.message==='key_protection_unavailable'?0:3));`,require.resolve('../reference/offline-notes.cjs'),dir,id],{env:{...process.env,DBUS_SESSION_BUS_ADDRESS:'unix:path=/nonexistent-freefsm-secret-service'},timeout:15000});
  assert.equal(unavailable.status,0,'unavailable protection did not fail closed');
  const unavailableLogout=spawnSync(process.execPath,['-e',`const {ProtectedStore}=require(process.argv[1]); new ProtectedStore(process.argv[2],process.argv[3]).destroy().then(()=>process.exit(2)).catch(e=>process.exit(e.message==='key_protection_unavailable'?0:3));`,require.resolve('../reference/offline-notes.cjs'),dir,id],{env:{...process.env,DBUS_SESSION_BUS_ADDRESS:'unix:path=/nonexistent-freefsm-secret-service'},timeout:15000});
  assert.equal(unavailableLogout.status,0,'unavailable key deletion falsely acknowledged');assert(!existsSync(join(dir,'cache')));
  await store.save({token:'secret-bearer',pending:'private-notes'});
  const actualKey=store.key();
  try{store.secret('store',randomBytes(32).toString('hex'));await assert.rejects(store.load(),/protected_store_invalid/);}finally{store.secret('store',actualKey.toString('hex'));actualKey.fill(0);}
  const original=readFileSync(join(dir,'cache')),corrupt=Buffer.from(original);corrupt[corrupt.length-1]^=1;writeFileSync(join(dir,'cache'),corrupt);
  await assert.rejects(store.load(),/protected_store_invalid/);writeFileSync(join(dir,'cache'),original);
  const wrongDir=mkdtempSync(join(tmpdir(),'offline-wrong-')), wrong=new ProtectedStore(wrongDir,randomUUID()); await wrong.create();
  try { await assert.rejects(new ProtectedStore(dir,wrong.id).load(),/protected_store_invalid/); } finally { await wrong.destroyKey();rmSync(wrongDir,{recursive:true,force:true}); }
  writeFileSync(join(dir,'cache'),Buffer.from('corrupt ciphertext'));
  const logout=spawnSync(process.execPath,[require.resolve('../reference/offline-notes.cjs')],{input:JSON.stringify({directory:dir,store_id:id,server:'https://offline.test/api/v1',action:'logout'}),timeout:15000});
  assert.equal(logout.status,0,'logout must destroy even corrupt ciphertext');
  destroyed=true;
  assert(!existsSync(join(dir,'cache')));
  await assert.rejects(store.load(),/key_protection_unavailable/);
 } finally { if(!destroyed)await store.destroy(); rmSync(dir,{recursive:true,force:true}); }
});

test('failed filesystem purge or restore lock cannot revive old cache after restart', async()=>{
 for(const outcome of ['unauthorized','instance_restored','offline_disabled','forbidden']){
  const dir=mkdtempSync(join(tmpdir(),'offline-fault-')),store=new ProtectedStore(dir,randomUUID());
  try{
   await store.create();await store.save({token:'credential',lease:{expires_at:'2999-01-01T00:00:00Z'},issuance:{id:'issuance',state:'unknown'},synced:true,restore:false,highWater:0,cache:{job:{title:'restricted'}},notes:{},pending:{operation:{job:'job',state:'unknown',intent:{field_notes:'recoverable'}}}});
   const originalSave=store.save.bind(store);
   store.save=async s=>{if(s.token===null)chmodSync(dir,0o500);return originalSave(s);};
   const issuanceDenial=['offline_disabled','forbidden'].includes(outcome);
   const c=new OfflineNotes(store,async()=>issuanceDenial?{status:200,data:{operation_id:'issuance',code:outcome}}:{status:401,error:outcome==='instance_restored'?{code:outcome,recovery_disposition:'locked_review'}:{code:outcome,recovery_disposition:'purge'}});
   await assert.rejects(issuanceDenial?c.authenticate('credential'):c.reconcile('operation'),/protected_store_locked/,outcome);
   chmodSync(dir,0o700);
   await assert.rejects(new OfflineNotes(new ProtectedStore(dir,store.id),async()=>{}).view(),/store_busy/);
   assert(existsSync(join(dir,'.writer')),'failed authority write lost durable quarantine');
   assert(!readFileSync(join(dir,'cache')).includes('recoverable'));
  }finally{chmodSync(dir,0o700);await store.destroy();rmSync(dir,{recursive:true,force:true});}
 }
});

test('lease exact expiry, original intent, receipt before retry and restore lock', async () => {
 const dir=mkdtempSync(join(tmpdir(),'offline-')), store=new ProtectedStore(dir,randomUUID());
 let now=Date.parse('2026-10-15T00:00:00Z');
 const calls=[]; let reply={status:404,error:{code:'not_found'}};
 const transport=async(path,method,body,auth)=>{calls.push({path,method,body});if(path==='/offline/policy')return {status:200,data:{enabled:true,hours:24}};if(path==='/offline/lease'&&reply.status===200)return {...reply,data:{...reply.data,code:'accepted',operation_id:auth.operation}};return reply;};
 const c=new OfflineNotes(store,transport,()=>now);
 try {
  await c.initialize();
  // Online authority is established through the transport, never by caller timestamps.
  reply={status:200,data:{id:'lease',issued_at:new Date(now).toISOString(),expires_at:new Date(now+86400000).toISOString(),epoch:'epoch',user_id:'user'}};
  await c.authenticate('bearer');
  reply={status:200,data:{records:[{id:'job',title:'Job'}],removed:[],complete:true,completion_cursor:'cursor',next_page:null,purge:'none'}};
  await c.synchronize();
  reply={status:200,data:{field_notes:'before',notes_revision:'revision',can_edit_notes:true}};
  await c.readNotes('job');
  const id=await c.stage('job','after');
  assert.equal((await c.view()).pending[id].state,'pending');
  now+=86400000;
  await assert.rejects(c.view(),/authorization_lease_expired/);
  await assert.rejects(c.stage('job','later'),/authorization_lease_expired/);
  now-=1;
  await assert.rejects(c.view(),/authorization_lease_expired/,'observed expiry must remain locked after a clock rollback');
  now+=1;
  reply={status:200,data:{id:'renewed',issued_at:new Date(now).toISOString(),expires_at:new Date(now+86400000).toISOString(),epoch:'epoch',user_id:'user'}};
  await c.authenticate('bearer');
  reply={status:200,data:{records:[{id:'job',title:'Job'}],removed:[],complete:true,completion_cursor:'renewed-cursor',next_page:null,purge:'none'}};
  await c.synchronize();
  reply={status:503,error:{code:'unavailable'}}; await c.submit(id);
  reply={status:404,error:{code:'not_found'}}; await c.reconcile(id);
  await assert.rejects(c.view(),/locked_review/);
  reply={status:200,data:{records:[{id:'job',title:'Job'}],removed:[],complete:true,completion_cursor:'receipt-refresh',next_page:null,purge:'none'}};
  await c.synchronize();
  assert.equal((await c.view()).pending[id].intent.operation_id,id);
  await assert.rejects(c.submit(id),/retry_after_required/,'explicit retry must honor server dependency backoff');
  now+=5000;
  reply={status:401,error:{code:'instance_restored',recovery_disposition:'locked_review'}};
  await assert.rejects(c.reconcile(id),/locked_review/); await assert.rejects(c.view(),/locked_review/);
  const recovered=new OfflineNotes(new ProtectedStore(dir,store.id),transport,()=>now);
  await assert.rejects(recovered.submit(id),/locked_review/);
  assert.equal(calls.filter(x=>x.method==='POST'&&x.path.includes('field-notes')).length,1);
 } finally { await c.logout(); rmSync(dir,{recursive:true,force:true}); }
});
