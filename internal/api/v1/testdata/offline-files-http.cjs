'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs/promises');
const os=require('node:os');
const path=require('node:path');
const {randomUUID}=require('node:crypto');
const {OfflineFiles,ProtectedStore}=require('../reference/offline-files-client.cjs');
(async()=>{
 const input=JSON.parse(require('node:fs').readFileSync(0,'utf8'));
 const directory=await fs.mkdtemp(path.join(os.tmpdir(),'offline-files-')),identity='files-'+randomUUID();
 let store=new ProtectedStore(directory,identity),lose=false,breakPersistence=false,submissions=0;
 const transport=async(p,method='GET',body,auth)=>{
  const headers={Authorization:'Bearer '+auth.token};if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;if(auth.operation)headers['Operation-ID']=auth.operation;
  if(body!==undefined)headers['Content-Type']=Buffer.isBuffer(body)?'application/octet-stream':'application/json';
  if(p==='/sync/operations'){submissions++;assert(!JSON.stringify(body).includes('private-byte-payload'));if(lose){lose=false;headers['X-Lose-File-Response']='yes';}}
  const response=await fetch(input.url+p,{method,headers,body:body===undefined?undefined:Buffer.isBuffer(body)?body:JSON.stringify(body)});
  const parsed=await response.json();
  if(breakPersistence&&p.startsWith('/sync/jobs')){breakPersistence=false;await fs.chmod(directory,0o500);}
  return {...parsed,status:response.status};
 };
 let client=new OfflineFiles(store,transport);
 try{
  await client.initialize();await client.authenticate(input.token);await client.synchronize();
  let state=await client.view(),job=Object.keys(state.cache)[0];
  const publication=await client.stageFilePublication(job,'private-name.txt',Buffer.from('private-byte-payload'),[]);
  assert.equal(submissions,0);assert(!(await fs.readFile(path.join(directory,'cache'))).includes(Buffer.from('private-byte-payload')));
  await client.transferFile(publication);
  await client.synchronize();assert.equal((await client.view()).cache[job].files.length,0,'transfer published early');
  lose=true;await assert.rejects(client.submitBatch([publication]));assert.equal(submissions,1);
  store=new ProtectedStore(directory,identity);client=new OfflineFiles(store,transport);
  assert.equal((await client.reconcile(publication)).state,'accepted');assert.equal(submissions,1,'receipt recovery resubmitted');
  const damaged=await client.stageFilePublication(job,'digest.txt',Buffer.from('digest-specific bytes'),[]);await client.transferFile(damaged);
  const original=(await client.view()).pending[damaged].file;
  await fetch(input.url+'/__fixture/corrupt-transfer',{method:'POST',body:JSON.stringify({id:original.target})});
  await client.submitBatch([damaged]);assert.equal((await client.view()).pending[damaged].receipt.code,'invalid_file');
  const repairedDigest=await client.repairFile(damaged,{object_type:original.object_type,object_id:original.object_id},[]);
  assert.notEqual(repairedDigest,damaged);await client.transferFile(repairedDigest);await client.submitBatch([repairedDigest]);
  await client.synchronize();state=await client.view();let file=state.cache[job].files[0];
  const rename=await client.stageFileMutation(job,file.id,'rename','client.txt',[]);
  await transport('/files/'+file.id+'/rename','POST',{operation_id:randomUUID(),object_type:file.object_type,object_id:Number(file.object_id),revision:file.revision,name:'server.txt'},{token:input.token});
  await client.submitBatch([rename]);assert.equal((await client.view()).pending[rename].state,'terminal');
  await client.synchronize();file=(await client.view()).cache[job].files[0];
  const repaired=await client.repairFile(rename,{name:'reviewed.txt',revision:file.revision},[]);assert.notEqual(repaired,rename);
  await client.submitBatch([repaired]);await client.synchronize();file=(await client.view()).cache[job].files[0];assert.equal(file.name,'reviewed.txt');
  const remove=await client.stageFileMutation(job,file.id,'remove',undefined,[]);
  const restored=await (await fetch(input.url+'/__fixture/restore',{method:'POST'})).json();
  await assert.rejects(client.synchronize(),/locked_review/);
  await client.authenticate(restored.token);await client.synchronize();
  await assert.rejects(client.submitBatch([remove]),/reconciliation_or_review_required/);
  await client.reconcile(remove);await client.acknowledgeUncertain(remove);
  const reviewed=await client.repairFile(remove,{revision:file.revision},[]);
  await client.submitBatch([reviewed]);await client.synchronize();assert((await client.view()).cache[job].files[0].removed_at);
  const pending=await client.stageFilePublication(job,'purge-name.txt',Buffer.from('purge-byte-secret'),[]);
  await fetch(input.url+'/__fixture/loss',{method:'POST'});await client.synchronize();
  const purged=await client.view();assert.equal(purged.pending[pending].state,'purged');assert(!JSON.stringify(purged).includes('purge-name'));assert(!JSON.stringify(purged).includes('cHVyZ2UtYnl0ZS1zZWNyZXQ='));
  await fetch(input.url+'/__fixture/regrant',{method:'POST'});await client.synchronize();
  await client.stageFilePublication(job,'quarantine-name.txt',Buffer.from('quarantine-bytes'),[]);
  await fetch(input.url+'/__fixture/loss',{method:'POST'});breakPersistence=true;
  await assert.rejects(client.synchronize(),/protected_store_locked/);await fs.chmod(directory,0o700);
  const fresh=new OfflineFiles(new ProtectedStore(directory,identity),transport);await assert.rejects(fresh.view(),/store_busy|protected_store_locked/);
  assert(pending);console.log('PASS encrypted File publication, separate bytes, persisted response-loss receipt recovery, stale/new-ID repair, restore quarantine and failed scope-purge quarantine');
 }finally{await fs.chmod(directory,0o700);await store.destroy();await fs.rm(directory,{recursive:true,force:true});}
})().catch(e=>{console.error(e);process.exitCode=1;});
