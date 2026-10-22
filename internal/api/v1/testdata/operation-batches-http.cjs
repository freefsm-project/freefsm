'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const {join}=require('node:path');
const {tmpdir}=require('node:os');
const {randomUUID}=require('node:crypto');
const {spawnSync}=require('node:child_process');
const {OfflineNotes,ProtectedStore}=require('../reference/offline-notes.cjs');
(async()=>{
 const input=JSON.parse(fs.readFileSync(0,'utf8'));
 const directory=input.directory||fs.mkdtempSync(join(tmpdir(),'batch-http-'));
 const store=new ProtectedStore(directory,input.store_id||randomUUID());
 let interrupt=false,posts=0;
 const transport=async(path,method,body,auth)=>{
  const headers={Authorization:'Bearer '+auth.token};if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;if(auth.operation)headers['Operation-ID']=auth.operation;
  if(body!==undefined)headers['Content-Type']='application/json';
  if(path==='/sync/operations'&&method==='POST'){posts++;if(interrupt){headers['X-Batch-Interrupt']='yes';interrupt=false;}}
  const r=await fetch(input.url+path,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});return {status:r.status,...await r.json()};
 };
 const client=new OfflineNotes(store,transport);
 if(input.restart){
  const [first,second]=input.ids;
  await assert.rejects(client.submitBatch(input.ids),/reconciliation_or_review_required/);
  assert.equal((await client.reconcile(first)).state,'accepted');assert.equal((await client.reconcile(second)).state,'pending');
  assert.equal(posts,0,'receipt-first restart must not submit');
   assert.deepEqual((await store.load()).pending[second].intent,input.intent);
   await assert.rejects(client.view(),/locked_review/);await client.synchronize();
  await client.submitBatch([second]);assert.equal(posts,1);
  assert.equal((await client.view()).pending[second].state,'accepted');return;
 }
 try{
  await client.initialize();await client.authenticate(input.token);await client.synchronize();
  const job=Object.keys((await client.view()).cache)[0];assert(job);await client.readNotes(job);
  // Same-state no-ops preserve the original revision while proving ordered dependency acceptance.
  const first=await client.stageBatch(job,'',[]),second=await client.stageBatch(job,'',[first]);
  const intent=(await client.view()).pending[second].intent;
  interrupt=true;await assert.rejects(client.submitBatch([first,second]));
  assert.equal((await store.load()).pending[first].state,'unknown');
  const child=spawnSync(process.execPath,[__filename],{input:JSON.stringify({...input,directory,store_id:store.id,restart:true,ids:[first,second],intent}),encoding:'utf8',timeout:30000});
   assert.equal(child.status,0,child.stderr);
   await client.readNotes(job);
  const bad=await client.stageBatch(job,'never saved',[randomUUID()]);
  const blocked=await client.stageBatch(job,'blocked text',[bad]);
  const unrelated=await client.stageBatch(job,'',[]);
  await client.submitBatch([bad,blocked,unrelated]);
  let view=await client.view();
  assert.equal(view.pending[bad].receipt.code,'invalid_dependency');assert.equal(view.pending[blocked].receipt.code,'failed_dependency');assert.equal(view.pending[unrelated].state,'accepted');
  const revision=(await client.readNotes(job)).notes_revision;
  await assert.rejects(client.repair(bad,'',revision),/review_required/);
  const repair=await client.repair(bad,'',revision,[]);
  assert.deepEqual((await client.view()).pending[blocked].batch.depends_on,[bad],'repair must never rewrite descendants');
  const repairedChild=await client.repair(blocked,'',revision,[repair]);
  await client.submitBatch([repair,repairedChild]);
  await assert.rejects(client.repair(unrelated,'',revision,[]),/review_required/);
  const hundred=[];for(let i=0;i<101;i++)hundred.push(await client.stageBatch(job,'',[]));
  const before=posts;await assert.rejects(client.submitBatch(hundred),/batch_limit/);assert.equal(posts,before);
  await client.submitBatch(hundred.slice(0,100));assert.equal((await client.view()).pending[hundred[99]].state,'accepted');await client.discard(hundred[100]);
  const probe=await client.stageBatch(job,'',[]),entry=(await client.view()).pending[probe];
  const overhead=Buffer.byteLength(JSON.stringify({operations:[{...entry.intent,...entry.batch}]}));await client.discard(probe);
  const over=await client.stageBatch(job,'x'.repeat(1048577-overhead),[]);
  const beforeSize=posts;await assert.rejects(client.submitBatch([over]),/batch_limit/);assert.equal(posts,beforeSize);await client.discard(over);
  const exact=await client.stageBatch(job,'x'.repeat(1048576-overhead),[]);
  await client.submitBatch([exact]);assert.equal((await client.view()).pending[exact].state,'accepted');await client.readNotes(job);
  const pending=await client.stageBatch(job,'restore pending',[]);
  await fetch(input.url+'/__fixture/restore',{method:'POST'});
  await assert.rejects(client.submitBatch([pending]),/locked_review/);
  const fresh=await (await fetch(input.url+'/__fixture/token',{method:'POST'})).json();
  await client.authenticate(fresh.token);await assert.rejects(client.submitBatch([pending]),/locked_review/);await client.synchronize();
  await assert.rejects(client.submitBatch([pending]),/reconciliation_or_review_required/);
  assert.equal((await client.reconcile(pending)).state,'restore_uncertain');
  assert(!fs.readFileSync(join(directory,'cache')).includes('restore pending'));
  console.log('PASS encrypted batch restart, partial interruption, receipt-first retry, dependencies, reviewed descendant repair and restore lock');
 }finally{await store.destroy();fs.rmSync(directory,{recursive:true,force:true});}
})().catch(e=>{console.error(e);process.exitCode=1;});
