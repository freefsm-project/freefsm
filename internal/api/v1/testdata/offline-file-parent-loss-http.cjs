'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs/promises');
const os=require('node:os');
const path=require('node:path');
const {randomUUID}=require('node:crypto');
const {OfflineFiles,ProtectedStore}=require('../reference/offline-files-client.cjs');
(async()=>{
 const input=JSON.parse(require('node:fs').readFileSync(0,'utf8')),scenario=input.scenario[0];
 const directory=await fs.mkdtemp(path.join(os.tmpdir(),'file-parent-loss-')),identity='parent-'+randomUUID();
 let store=new ProtectedStore(directory,identity),client,token=input.token,cancelSnapshot=false,observed;
 const writes=[];
 const transport=async(p,method='GET',body,auth)=>{
  const headers={Authorization:'Bearer '+auth.token};if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;if(auth.operation)headers['Operation-ID']=auth.operation;
  if(body!==undefined)headers['Content-Type']=Buffer.isBuffer(body)?'application/octet-stream':'application/json';
  if(method!=='GET')writes.push({p,body});
  const response=await fetch(input.url+p,{method,headers,body:body===undefined?undefined:Buffer.isBuffer(body)?body:JSON.stringify(body)});
  const result={...await response.json(),status:response.status};
  if(p.startsWith('/sync/jobs')&&response.status===200){observed=result.data;if(cancelSnapshot){cancelSnapshot=false;throw Error('cancelled_snapshot');}}
  return result;
 };
 const reopen=()=>{store=new ProtectedStore(directory,identity);client=new OfflineFiles(store,transport);};
 const fixture=async name=>{const r=await fetch(input.url+'/__fixture/'+name,{method:'POST'});assert(r.ok);return r;};
 try{
  reopen();await client.initialize();await client.authenticate(token);await client.synchronize();
  const initial=await client.view(),job=Object.keys(initial.cache)[0],parent=initial.cache[job].file_parents.find(p=>p.object_type==='estimate');assert(parent?.can_upload);
  await client.readNotes(job);
  const doc=await client.stageFilePublication(job,'lost-document-name.txt',Buffer.from('lost-document-bytes'),[],parent);
  const note=await client.stageBatch(job,'independent authorized Notes',[]);
  const jobFile=await client.stageFilePublication(job,'independent-job-name.txt',Buffer.from('independent Job bytes'),[]);
  const dependent=await client.stageBatch(job,'dependent text must not apply',[doc]);
  const before=(await client.view()).pending;
  const publishedRejection=scenario==='publication'||scenario==='publication-receipt';
  if(publishedRejection)await client.transferFile(doc);
  await fixture('document-loss');
  if(scenario==='restore'){
   token=(await (await fixture('restore')).json()).token;
   await assert.rejects(client.synchronize(),/locked_review/);
   await client.authenticate(token);
   cancelSnapshot=true;await assert.rejects(client.synchronize(),/cancelled_snapshot/);
   reopen();await assert.rejects(client.view(),/locked_review/,'cancelled restore sync disclosed cached content');
  }else if(scenario==='transport'){
   await assert.rejects(client.transferFile(doc),/inaccessible_target/);
  }else if(scenario==='publication'){
   await client.submitBatch([doc]);
  }else if(scenario==='publication-receipt'){
   const rejected=await transport('/sync/operations','POST',{operations:[{...before[doc].intent,...before[doc].batch}]},{token});
   assert.equal(rejected.status,200);assert.equal(rejected.data.results[0].receipt.code,'not_found');
   const recovered=await client.reconcile(doc);assert.equal(recovered.receipt.code,'not_found');
  }else if(scenario==='receipt'){
   const e=before[doc];
   const rejected=await transport('/file-transfers','POST',{id:e.file.target,publication_id:doc,object_type:e.intent.object_type,object_id:e.intent.object_id,name:e.upload.name,size:e.upload.size,sha256:e.upload.sha256},{token,operation:e.transport.start.id});
   assert.equal(rejected.status,404);
   const recovered=await client.reconcileFileTransfer(doc,e.transport.start.id);assert.equal(recovered.receipt.code,'not_found');
  }
  const count=writes.length;
  await client.synchronize();assert.equal(writes.length,count,'synchronization replayed pending work');
  assert.equal(observed.purge,'none');assert.equal(observed.complete,true);
  assert(observed.records.some(r=>r.id===job));assert(!observed.records.find(r=>r.id===job).file_parents.some(p=>p.object_type===parent.object_type&&p.object_id===parent.object_id));
  reopen();const after=await client.view();assert(after.cache[job],'authorized Job lost');
  assert.equal(after.pending[doc].state,'purged');assert(!after.pending[doc].intent&&!after.pending[doc].upload&&!after.pending[doc].file);
  assert(!JSON.stringify(after).includes('lost-document-name'));assert(!JSON.stringify(after).includes(Buffer.from('lost-document-bytes').toString('base64')));
  for(const id of [note,jobFile,dependent]){
   assert.equal(after.pending[id].state,scenario==='restore'?'restore_uncertain':'pending','unrelated authorized intent destroyed');
   assert.deepEqual(after.pending[id].intent,before[id].intent);assert.deepEqual(after.pending[id].batch,before[id].batch);
  }
  assert.deepEqual(after.pending[jobFile].upload,before[jobFile].upload,'independent encrypted bytes lost');
  assert.deepEqual(after.pending[dependent].batch.depends_on,[doc],'dependent silently retargeted');
  await assert.rejects(client.transferFile(doc),/review_required/);
  await assert.rejects(client.repairFile(doc,parent,[]),/review_required/,'inaccessible original parent followed conversion');
  if(scenario==='restore'){
   const count=writes.length;await assert.rejects(client.submitBatch([note]),/reconciliation_or_review_required/);await assert.rejects(client.transferFile(jobFile),/review_required/);assert.equal(writes.length,count);
   await client.reconcile(note);await client.synchronize();await client.acknowledgeUncertain(note);const current=await client.readNotes(job);
   const reviewed=await client.repair(note,'independent authorized Notes',current.notes_revision,[]);assert.notEqual(reviewed,note);await client.submitBatch([reviewed]);
  }else{
   await client.transferFile(jobFile);await client.submitBatch([note,dependent,jobFile]);
   const done=(await client.view()).pending;assert.equal(done[note].state,'accepted');assert.equal(done[jobFile].state,'accepted');assert.equal(done[dependent].state,'terminal');assert.equal(done[dependent].receipt.code,publishedRejection?'failed_dependency':'invalid_dependency');
  }
  // Actual whole-Job loss must still remove every associated pending intent.
  await client.readNotes(job);const whole=await client.stageBatch(job,'whole Job loss secret',[]);
  await fixture('loss');await client.synchronize();reopen();const gone=await client.view();assert(!gone.cache[job]);assert.equal(gone.pending[whole].state,'purged');assert(!gone.pending[whole].intent);
  assert(!(await fs.readFile(path.join(directory,'cache'))).includes(Buffer.from('independent authorized Notes')));
  console.log('PASS precise File parent loss '+scenario+' with independent encrypted intent and whole-Job guard');
 }finally{await store.destroy();await fs.rm(directory,{recursive:true,force:true});}
})().catch(e=>{console.error(e);process.exitCode=1;});
