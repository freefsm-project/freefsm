'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs/promises');
const path=require('node:path');
const os=require('node:os');
const {randomUUID}=require('node:crypto');
const {execFileSync}=require('node:child_process');
const {OfflineFiles,ProtectedStore}=require('../reference/offline-files-client.cjs');
const input=JSON.parse(require('node:fs').readFileSync(0,'utf8'));
async function worker(){
 const calls=[];let lost=false,throttled=false;
 const store=new ProtectedStore(input.directory,input.identity);
 const transport=async(p,method='GET',body,auth)=>{
  calls.push({p,method,operation:auth.operation,bytes:Buffer.isBuffer(body)?body.toString('base64'):undefined});
  if(input.lookupUnavailable&&p.startsWith('/file-transfer-operations/'))return {status:503,error:{code:'unavailable',recovery_disposition:'retry',retry_after_seconds:5}};
  if(!throttled&&((input.throttle===true&&method==='PUT')||(input.throttle==='limits'&&p==='/files/limits'))){throttled=true;return {status:429,error:{code:'rate_limited',recovery_disposition:'retry',retry_after_seconds:10}};}
  const headers={Authorization:'Bearer '+auth.token};if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;if(auth.operation)headers['Operation-ID']=auth.operation;
  if(body!==undefined)headers['Content-Type']=Buffer.isBuffer(body)?'application/octet-stream':'application/json';
  if(!lost&&((input.lose==='start'&&p==='/file-transfers')||(input.lose==='chunk'&&method==='PUT'))){lost=true;headers['X-Lose-File-Response']='yes';}
  const r=await fetch(input.url+p,{method,headers,body:body===undefined?undefined:Buffer.isBuffer(body)?body:JSON.stringify(body)});
  const result={...await r.json(),status:r.status};
  // Smaller advertised segments exercise changing discovery without sending
  // chunks above the real production limit. Retry must retain staged length.
  if(p==='/files/limits'&&input.chunkSize)result.data.max_chunk_bytes=input.chunkSize;
  return result;
 };
 const client=new OfflineFiles(store,transport,()=>Date.now()+(input.advance||0));
 try{const result=await client[input.action](...(input.args||[]));console.log(JSON.stringify({result,calls}));}
 catch(e){console.log(JSON.stringify({error:e.message,calls}));}
}
async function main(){
 const directory=await fs.mkdtemp(path.join(os.tmpdir(),'file-transport-')),identity='transport-'+randomUUID();
 const store=new ProtectedStore(directory,identity);
 let advance=0;
 const run=(action,args=[],options={})=>JSON.parse(execFileSync(process.execPath,[__filename,'worker'],{input:JSON.stringify({...input,directory,identity,action,args,advance,...options}),encoding:'utf8'}));
 const ok=(action,args=[],options={})=>{const r=run(action,args,options);assert.equal(r.error,undefined,JSON.stringify(r));return r;};
 try{
  ok('initialize');ok('authenticate',[input.token]);ok('synchronize');
  const job=Object.keys(ok('view').result.cache)[0];
  // Stage through the real encrypted API in this parent; every transfer attempt
  // and recovery below executes in a new process with the real Secret Service key.
  const client=new OfflineFiles(store,async()=>{throw Error('staging must be offline');},()=>Date.now()+advance);
  const id=await client.stageFilePublication(job,'recovery.txt',Buffer.from('original exact encrypted segment'),[]);
  assert(!(await fs.readFile(path.join(directory,'cache'))).includes(Buffer.from('original exact encrypted segment')));
  let r=run('transferFile',[id],{lose:'start'});assert(r.error);const start=r.calls.find(c=>c.p==='/file-transfers');assert(start);
  r=run('transferFile',[id],{lookupUnavailable:true});
  assert(r.calls.some(c=>c.p==='/file-transfer-operations/'+start.operation),'missing receipt-first lookup');
  assert(!r.calls.some(c=>c.method!=='GET'),'unavailable receipt replayed a write');
  advance+=6000;
  ok('synchronize');r=ok('transferFile',[id]);
  assert(r.calls.some(c=>c.p==='/file-transfer-operations/'+start.operation));assert(!r.calls.some(c=>c.method!=='GET'));
  ok('synchronize');r=run('transferFile',[id],{lose:'chunk'});assert(r.error);const chunk=r.calls.find(c=>c.method==='PUT');assert(chunk);
  assert(!r.calls.some(c=>c.p==='/file-transfers'),'accepted start replayed');
  r=run('transferFile',[id],{lookupUnavailable:true});assert(!r.calls.some(c=>c.method!=='GET'));assert(r.calls.some(c=>c.p==='/file-transfer-operations/'+chunk.operation));
  advance+=6000;
  ok('synchronize');r=ok('transferFile',[id]);assert(!r.calls.some(c=>c.method!=='GET'));assert(r.calls.some(c=>c.p==='/file-transfer-operations/'+chunk.operation));
  ok('synchronize');r=ok('transferFile',[id]);assert.equal(r.result.state,'transferred_not_published');assert(!r.calls.some(c=>c.method==='PUT'||c.p==='/file-transfers'));
  ok('synchronize');assert.equal(ok('view').result.cache[job].files.length,0,'transport published File');
  const retry=await client.stageFilePublication(job,'throttle.txt',Buffer.from('exact-segment-and-remainder'),[]);
  r=run('transferFile',[retry],{throttle:true,chunkSize:8});assert.equal(r.error,'retry_after_required');const original=r.calls.find(c=>c.method==='PUT');
  r=run('transferFile',[retry]);assert.equal(r.error,'retry_after_required');assert(!r.calls.some(c=>c.p.startsWith('/file-transfer')),'retry deadline lost across restart');
  advance+=11000;
  r=ok('transferFile',[retry]);assert(r.calls.some(c=>c.p==='/file-transfer-operations/'+original.operation));assert(!r.calls.some(c=>c.method!=='GET'));
  ok('synchronize');r=ok('transferFile',[retry],{chunkSize:16});const replay=r.calls.find(c=>c.method==='PUT');
  assert.deepEqual(replay,original,'retry changed original UUID/offset/bytes/length');assert.equal(r.result.state,'transferred_not_published');

  const invalid=await client.stageFilePublication(job,'not-an-image.png',Buffer.from('plain text is not PNG'),[]);
  r=run('transferFile',[invalid]);assert(r.error);const rejected=ok('view').result.pending[invalid];
  assert.equal(rejected.state,'terminal','terminal verification rejection remained retryable');
  r=run('transferFile',[invalid]);assert(r.error);assert(!r.calls.some(c=>c.method!=='GET'),'terminal bytes replayed');
  const repaired=ok('repairFile',[invalid,{object_type:rejected.file.object_type,object_id:rejected.file.object_id,name:'reviewed-text.txt'},[]]).result;
  assert.notEqual(repaired,invalid);const repairedState=ok('view').result.pending[repaired];
  assert.notEqual(repairedState.file.target,rejected.file.target);assert.notEqual(repairedState.transport.start.id,rejected.transport.start.id);
  assert.equal(ok('transferFile',[repaired]).result.state,'transferred_not_published');

  // A real server-side start conflict is terminal, independently of publication.
  const conflict=await client.stageFilePublication(job,'original.txt',Buffer.from('conflict bytes'),[]);
  const staged=ok('view').result.pending[conflict];
  const occupied=await fetch(input.url+'/file-transfers',{method:'POST',headers:{Authorization:'Bearer '+input.token,'Content-Type':'application/json','Operation-ID':randomUUID()},body:JSON.stringify({id:staged.file.target,publication_id:conflict,object_type:staged.intent.object_type,object_id:staged.intent.object_id,name:'other.txt',size:staged.upload.size,sha256:staged.upload.sha256})});
  assert.equal(occupied.status,201);
  r=run('transferFile',[conflict]);assert.equal(r.error,'review_required');assert.equal(ok('view').result.pending[conflict].state,'terminal');
  r=ok('reconcileFileTransfer',[conflict,staged.transport.start.id]);assert.equal(r.result.receipt.code,'file_conflict');
  ok('synchronize');r=run('transferFile',[conflict]);assert(r.error);assert(!r.calls.some(c=>c.method!=='GET'));
  const replacement=ok('repairFile',[conflict,{object_type:staged.file.object_type,object_id:staged.file.object_id},[]]).result;
  assert.notEqual(replacement,conflict);assert.equal(ok('transferFile',[replacement]).result.state,'transferred_not_published');
  const limited=await client.stageFilePublication(job,'limits.txt',Buffer.from('discovery retry'),[]);
  r=run('transferFile',[limited],{throttle:'limits'});assert.equal(r.error,'retry_after_required');
  r=run('transferFile',[limited]);assert.equal(r.error,'retry_after_required');assert(!r.calls.some(c=>c.p==='/files/limits'||c.method!=='GET'));
  advance+=11000;assert.equal(ok('transferFile',[limited]).result.state,'transferred_not_published');

  // Accepted receipt after original-parent loss is not authority to resume.
  const lostParent=await client.stageFilePublication(job,'scope-secret.txt',Buffer.from('scope-byte-secret'),[]);
  r=run('transferFile',[lostParent],{lose:'start'});assert(r.error);const scopedStart=r.calls.find(c=>c.p==='/file-transfers').operation;
  await fetch(input.url+'/__fixture/loss',{method:'POST'});
  r=ok('reconcileFileTransfer',[lostParent,scopedStart]);assert.equal(r.result.receipt.code,'accepted');assert(!r.calls.some(c=>c.method!=='GET'));
  assert.equal(run('transferFile',[lostParent]).error,'locked_review');
  ok('synchronize');const purged=ok('view').result.pending[lostParent];assert.equal(purged.state,'purged');assert(!JSON.stringify(purged).includes('scope-secret'));assert(!purged.upload);
  r=ok('reconcileFileTransfer',[lostParent,scopedStart]);assert.equal(r.result.receipt.code,'accepted');
  assert.equal(r.result.state,'accepted');assert(!r.calls.some(c=>c.method!=='GET'));

  await fetch(input.url+'/__fixture/regrant',{method:'POST'});ok('synchronize');
  const restoredWork=await client.stageFilePublication(job,'restore-secret.txt',Buffer.from('restore-byte-secret'),[]);
  r=run('transferFile',[restoredWork],{lose:'start'});assert(r.error);const restoreStart=r.calls.find(c=>c.p==='/file-transfers').operation;
  const restored=await (await fetch(input.url+'/__fixture/restore',{method:'POST'})).json();
  r=run('transferFile',[restoredWork]);assert.equal(r.error,'locked_review');assert(!r.calls.some(c=>c.method!=='GET'));
  ok('authenticate',[restored.token]);ok('synchronize');
  r=ok('reconcileFileTransfer',[restoredWork,restoreStart]);assert.equal(r.result.state,'unknown','restore receipt miss became permission to retry');
  ok('synchronize');r=run('transferFile',[restoredWork]);assert(r.error);assert(!r.calls.some(c=>c.method!=='GET'));
  // Explicit publication reconciliation/review is still required to create any
  // replacement. A transport receipt alone never authorizes restored intent.
  ok('reconcile',[restoredWork]);ok('synchronize');ok('acknowledgeUncertain',[restoredWork]);
  const old=ok('view').result.pending[restoredWork];
  const reviewed=ok('repairFile',[restoredWork,{object_type:old.file.object_type,object_id:old.file.object_id},[]]).result;
  assert.notEqual(reviewed,restoredWork);assert.equal(ok('transferFile',[reviewed]).result.state,'transferred_not_published');
  const reviewedStart=ok('view').result.pending[reviewed].transport.start.id;
  const reauthorized=await (await fetch(input.url+'/__fixture/reauthorize',{method:'POST'})).json();
  r=run('transferFile',[reviewed]);assert.equal(r.error,'authorization_purged');assert(!r.calls.some(c=>c.method!=='GET'));
  ok('authenticate',[reauthorized.token]);ok('synchronize');
  const safe=ok('view').result.pending[reviewed];assert.equal(safe.state,'purged');assert(!safe.file&&!safe.upload&&!safe.intent);assert.equal(safe.transport.start.id,reviewedStart);
  assert(!JSON.stringify(safe).includes('restore-secret'));assert(!JSON.stringify(safe).includes('offset'));
  r=ok('reconcileFileTransfer',[reviewed,reviewedStart]);assert.equal(r.result.state,'accepted');assert(!r.result.receipt,'new client borrowed old receipt');
  console.log('PASS encrypted transport receipt-first start/chunk response loss and fresh-process recovery');
 }finally{await store.destroy();await fs.rm(directory,{recursive:true,force:true});}
}
(process.argv[2]==='worker'?worker():main()).catch(e=>{console.error(e);process.exitCode=1;});
