'use strict';
const assert=require('node:assert/strict'),fs=require('node:fs');
const {join}=require('node:path'),{tmpdir}=require('node:os'),{randomUUID}=require('node:crypto'),{spawnSync}=require('node:child_process');
const {OfflineNotes,ProtectedStore}=require('../reference/offline-notes.cjs');
const {OfflineClocks}=require('../reference/offline-clocks-client.cjs');
(async()=>{
 const input=JSON.parse(fs.readFileSync(0,'utf8')),directory=input.directory||fs.mkdtempSync(join(tmpdir(),'mixed-fields-'));
  const store=new ProtectedStore(directory,input.store_id||randomUUID());let lose=false,malform=false,posts=0,clockStore;
 const transport=async(path,method,body,auth)=>{
  const headers={Authorization:'Bearer '+auth.token};if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;if(auth.operation)headers['Operation-ID']=auth.operation;
  let raw=body===undefined?undefined:JSON.stringify(body);if(raw!==undefined)headers['Content-Type']='application/json';
  if(path==='/sync/operations'){posts++;if(malform){headers['X-Mixed-Malformed']='yes';raw=raw.replace('"value":"malformed"','"VALUE":"malformed"');malform=false;}}
  const r=await fetch(input.url+path,{method,headers,body:raw});const result={status:r.status,...await r.json()};
  if(lose&&path==='/sync/operations'){lose=false;throw Error('lost mixed committed response');}return result;
 };
 let client=new OfflineNotes(store,transport);
 if(input.restart){for(const id of input.ids)assert.equal((await client.reconcile(id)).state,'accepted');assert.equal(posts,0);return;}
 try{
  await client.initialize();await client.authenticate(input.token);await client.synchronize();
  const job=Object.keys((await client.view()).cache)[0];let row=(await client.view()).cache[job];
  assert(Array.isArray(row.status_definitions),'mixed snapshot must retain accepted Status definitions');
  assert(Array.isArray(row.subtasks));assert(row.custom_fields.can_edit);
  const target=row.status_definitions.find(v=>v.category==='job:completed'),task=row.subtasks[0];assert(target&&task);await client.readNotes(job);
  const status=await client.stageStatus(job,target.id,row.status_revision,target.meaning_revision,[]);
  const fields=await client.stageFields(job,[{definition_id:1,value:'composed reading'}],[status]);
  const subtask=await client.stageSubtask(job,task.id,true,task.completion_revision,[fields]);
  const notes=await client.stageBatch(job,'composed notes',[subtask]);const ids=[status,fields,subtask,notes];
  lose=true;await assert.rejects(client.submitBatch(ids),/lost mixed committed response/);
  const child=spawnSync(process.execPath,[__filename],{input:JSON.stringify({url:input.url,directory,store_id:store.id,restart:true,ids}),encoding:'utf8',timeout:30000});assert.equal(child.status,0,child.stderr);
  client=new OfflineNotes(new ProtectedStore(directory,store.id),transport);assert.equal(posts,1);
  await client.synchronize(true);row=(await client.view()).cache[job];
  assert.equal(row.status.category,'job:completed');assert.equal(row.subtasks[0].completed,true);assert.equal(row.field_notes,'composed notes');assert.equal(row.custom_fields.values[0].value,'composed reading');assert.equal(row.custom_fields.history.length,1);
  const fieldRevision=row.custom_fields.revisions['1'];await client.readNotes(job);
  const again=await client.stageStatus(job,target.id,row.status_revision,target.meaning_revision,[]);
  const invalid=await client.stageFields(job,[{definition_id:1,value:'invalid\ntext'}],[again]);
  const blocked=await client.stageSubtask(job,task.id,false,row.subtasks[0].completion_revision,[invalid]);
  const unrelated=await client.stageBatch(job,'unrelated notes survive',[]);await client.submitBatch([again,invalid,blocked,unrelated]);
  let view=await client.view();assert.equal(view.pending[invalid].receipt.reason,'invalid_text');assert.equal(view.pending[blocked].receipt.code,'failed_dependency');assert.equal(view.pending[unrelated].state,'accepted');
  await client.synchronize(true);row=(await client.view()).cache[job];assert.equal(row.custom_fields.revisions['1'],fieldRevision);assert.equal(row.custom_fields.history.length,1);assert.equal(row.subtasks[0].completed,true);assert.equal(row.field_notes,'unrelated notes survive');
  const currentNotes=await client.readNotes(job);await assert.rejects(client.repair(invalid,'wrong family',currentNotes.notes_revision,[]),/review_required/);
  const before=await client.stageBatch(job,'must not pass malformed envelope',[]),bad=await client.stageFields(job,[{definition_id:1,value:'malformed'}],[]);
  malform=true;await client.submitBatch([before,bad]);assert.equal((await client.reconcile(before)).state,'pending');assert.equal((await client.reconcile(bad)).state,'pending');
   assert.equal((await client.readNotes(job)).field_notes,'unrelated notes survive');await client.discard(before);await client.discard(bad);
   // Compose the public staged intents from both encrypted domain adapters into
   // one HTTP batch. Neither adapter may invent another family's target mapping.
   const clockDirectory=fs.mkdtempSync(join(tmpdir(),'mixed-clocks-'));
   clockStore=new ProtectedStore(clockDirectory,randomUUID());
   const clocks=new OfflineClocks(clockStore,transport);
   await clocks.initialize();await clocks.authenticate(input.token);await clocks.synchronize();
   const inside=await clocks.stageClock('in',{job_id:String(input.job_id)},[]);
   assert.equal((await clocks.submitBatch([inside]))[0].receipt.code,'accepted');await clocks.synchronize();
   const original=Object.values((await clocks.view()).clocks)[0];
   const outside=await clocks.stageClock('out',{entry_id:original.entry_id},[inside]);
   const mixedField=await client.stageFields(job,[{definition_id:1,value:'malformed'}],[outside]);
   const nextClock=await clocks.stageClock('in',{job_id:String(input.job_id)},[mixedField]);
   const pendingClocks=(await clocks.view()).pending,pendingFields=(await client.view()).pending;
   const wire=e=>({...e.intent,...e.batch});
   const mixed={operations:[wire(pendingClocks[outside]),wire(pendingFields[mixedField]),wire(pendingClocks[nextClock])]};
   malform=true;
   assert.equal((await transport('/sync/operations','POST',mixed,{token:input.token})).status,400);
   await clocks.synchronize(true);
   assert.equal(Object.values((await clocks.view()).clocks)[0].state,'active','later malformed field must not close the earlier clock');
   assert.equal((await clocks.reconcile(outside)).receipt,undefined);
   assert.equal((await client.reconcile(mixedField)).receipt,undefined);
   const mixedResult=await transport('/sync/operations','POST',mixed,{token:input.token});
   assert.deepEqual(mixedResult.data.results.map(v=>v.receipt.code),['accepted','accepted','accepted']);
   const replay=await transport('/sync/operations','POST',mixed,{token:input.token});
   assert.deepEqual(replay.data.results,mixedResult.data.results,'mixed-family receipts remain immutable on replay');
   assert.equal((await clocks.reconcile(outside)).receipt.entry_ref,original.entry_id);
   assert.equal((await client.reconcile(mixedField)).receipt.effect_id,mixedField);
   assert.equal((await clocks.reconcile(nextClock)).receipt.code,'accepted');
   await clocks.synchronize(true);await client.synchronize(true);
   const clockRows=Object.values((await clocks.view()).clocks),closed=clockRows.find(v=>v.entry_id===original.entry_id);
   assert.equal(clockRows.length,2);assert.equal(closed.state,'closed');assert.equal(closed.history.length,2);
   assert.equal(Date.parse(closed.clock_out),Date.parse(pendingClocks[outside].intent.clock.occurred_at));
   assert.equal(clockRows.filter(v=>v.state==='active').length,1,'old close-out replay must not close the newer entry');
   row=(await client.view()).cache[job];assert.equal(row.custom_fields.values[0].value,'malformed');assert.equal(row.custom_fields.history.length,2);
   const rejected=await client.stageFields(job,[{definition_id:1,value:'invalid\ntext'}],[]);
   const active=clockRows.find(v=>v.state==='active'),blockedOut=await clocks.stageClock('out',{entry_id:active.entry_id},[rejected]);
   const failed=await transport('/sync/operations','POST',{operations:[wire((await client.view()).pending[rejected]),wire((await clocks.view()).pending[blockedOut])]},{token:input.token});
   assert.deepEqual(failed.data.results.map(v=>v.receipt.code),['invalid_custom_field','failed_dependency']);
   await clocks.synchronize(true);assert.equal(Object.values((await clocks.view()).clocks).find(v=>v.entry_id===active.entry_id).state,'active');
   await client.authenticate(input.token);await client.synchronize();
  await fetch(input.url+'/__fixture/remove',{method:'POST'});await client.synchronize();view=await client.view();assert.equal(Object.keys(view.cache).length,0);
  for(const id of ids)assert.equal((await client.reconcile(id)).receipt.code,'accepted','safe receipt family survives purge');
  assert.equal((await client.reconcile(fields)).receipt.effect_id,fields);assert.equal((await client.reconcile(fields)).receipt.job_id,input.job_id);
   console.log('PASS mixed Status/Custom Fields/Subtask/Notes/Clock schema, cross-family dependencies and replay, exact preflight, encrypted restart/delta and safe receipt routing');
  }finally{if(clockStore){await clockStore.destroy();fs.rmSync(clockStore.directory,{recursive:true,force:true});}await store.destroy();fs.rmSync(directory,{recursive:true,force:true});}
})().catch(e=>{console.error(e);process.exitCode=1;});
