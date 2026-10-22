'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs/promises');
const path=require('node:path');
const os=require('node:os');
const {randomUUID}=require('node:crypto');
const {OfflineFiles,ProtectedStore}=require('../reference/offline-files-client.cjs');
(async()=>{
 const input=JSON.parse(require('node:fs').readFileSync(0,'utf8'));
 const directory=await fs.mkdtemp(path.join(os.tmpdir(),'file-noop-')),store=new ProtectedStore(directory,'noop-'+randomUUID());
 const transport=async(p,method='GET',body,auth)=>{
  const headers={Authorization:'Bearer '+auth.token};if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;if(auth.operation)headers['Operation-ID']=auth.operation;
  if(body!==undefined)headers['Content-Type']=Buffer.isBuffer(body)?'application/octet-stream':'application/json';
  const r=await fetch(input.url+p,{method,headers,body:body===undefined?undefined:Buffer.isBuffer(body)?body:JSON.stringify(body)});
  return {...await r.json(),status:r.status};
 };
 const client=new OfflineFiles(store,transport);
 try{
  await client.initialize();await client.authenticate(input.token);await client.synchronize();
  const job=Object.keys((await client.view()).cache)[0];
  const pub=await client.stageFilePublication(job,'original.txt',Buffer.from('no-op bytes'),[]);await client.transferFile(pub);await client.submitBatch([pub]);await client.synchronize();
  const file=(await client.view()).cache[job].files[0];
  const history=async()=>{const r=await transport('/jobs/'+file.object_id+'/activity','GET',undefined,{token:input.token});assert.equal(r.status,200);return r.data;};
  const before=await history();assert(before.records.length>0,'history fixture has no publication');
  const noop=await client.stageFileMutation(job,file.id,'rename',file.name,[]);await client.submitBatch([noop]);
  assert.equal((await client.view()).pending[noop].receipt.code,'accepted');
  await client.synchronize();assert.deepEqual((await client.view()).cache[job].files[0],file,'rename no-op rewrote File revision/attribution');assert.deepEqual(await history(),before,'rename no-op appended collaboration history');
  const remove=await client.stageFileMutation(job,file.id,'remove',undefined,[]),again=await client.stageFileMutation(job,file.id,'remove',undefined,[]);
  await client.submitBatch([remove]);await client.synchronize();
  const removed=(await client.view()).cache[job].files[0],removedHistory=await history();assert(removed.removed_at);assert.notDeepEqual(removedHistory,before,'real removal has no history');
  await client.submitBatch([again]);assert.equal((await client.view()).pending[again].receipt.code,'accepted');await client.synchronize();
  assert.deepEqual((await client.view()).cache[job].files[0],removed,'remove no-op rewrote original removal facts');assert.deepEqual(await history(),removedHistory,'remove no-op appended history');
  console.log('PASS encrypted File no-op history for rename and repeated removal');
 }finally{await store.destroy();await fs.rm(directory,{recursive:true,force:true});}
})().catch(e=>{console.error(e);process.exitCode=1;});
