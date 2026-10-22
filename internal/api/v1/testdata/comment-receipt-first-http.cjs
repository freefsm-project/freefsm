'use strict';
const assert=require('node:assert/strict'),fs=require('node:fs/promises'),os=require('node:os'),path=require('node:path');
const {randomUUID}=require('node:crypto');
const {execFile,spawnSync}=require('node:child_process'),{promisify}=require('node:util');
const {OfflineFieldClient,ProtectedStore}=require('../reference/offline-notes-client.cjs');
(async()=>{
 let input='';for await(const c of process.stdin)input+=c;const cfg=JSON.parse(input);
 const directory=cfg.directory||await fs.mkdtemp(path.join(os.tmpdir(),'comment-reconnect-'));await fs.chmod(directory,0o700);
 const identity=cfg.store_id||randomUUID(),store=new ProtectedStore(directory,identity);let interrupt=false,fault=false,posts=0;
 const transport=async(route,method,body,meta)=>{
  const headers={Authorization:'Bearer '+meta.token,'Content-Type':'application/json'};
  if(meta.epoch)headers['X-FreeFSM-Session-Epoch']=meta.epoch;if(meta.operation)headers['Operation-ID']=meta.operation;
  if(route==='/sync/operations'){posts++;if(interrupt){headers['X-Comment-Interrupt']='yes';interrupt=false;}}
  const response=await fetch(cfg.url+route,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
  const value=await response.json();
  if(fault&&route.startsWith('/comment-operations/')){
   fault=false;await fs.rename(path.join(directory,'cache'),path.join(directory,'cache-held'));await fs.mkdir(path.join(directory,'cache'));
  }
  return {status:response.status,...value};
 };
 const restart=()=>new OfflineFieldClient(new ProtectedStore(directory,identity),transport);let client=restart();
 const freshView=async()=>{const out=await promisify(execFile)(process.execPath,['-e',`const {OfflineFieldClient,ProtectedStore}=require(${JSON.stringify(path.resolve(__dirname,'../reference/offline-notes-client.cjs'))});new OfflineFieldClient(new ProtectedStore(process.argv[1],process.argv[2]),()=>{throw Error('no contact');}).view().then(()=>console.log('READABLE')).catch(e=>console.log(e.message));`,directory,identity]);return out.stdout.trim();};
 if(cfg.reconcile_child){
  fault=cfg.mode.endsWith('failure');const outcome={};
  try{outcome.result=await client.reconcile(cfg.operation_id);}catch(e){outcome.error=e.message;}
  try{await client.view();outcome.view='READABLE';}catch(e){outcome.view=e.message;}
  try{await client.stageComment(cfg.job,{...cfg.parent,content:'fresh process must not stage'},[]);outcome.stage='STAGED';}catch(e){outcome.stage=e.message;}
  try{await client.submitBatch([cfg.sibling]);outcome.submit='SUBMITTED';}catch(e){outcome.submit=e.message;}
  outcome.posts=posts;console.log(JSON.stringify(outcome));return;
 }
 const freshReconcile=(id,job,parent,sibling)=>{
  const child=spawnSync(process.execPath,[__filename],{input:JSON.stringify({url:cfg.url,mode:cfg.mode,directory,store_id:identity,reconcile_child:true,operation_id:id,job,parent,sibling}),encoding:'utf8',timeout:30000});
  assert.equal(child.status,0,child.stderr);const outcome=JSON.parse(child.stdout);assert.equal(outcome.posts,0,'fresh reconciliation process must not replay');return outcome;
 };
 try {
  await client.initialize();await client.authenticate(cfg.token);await client.synchronize();let state=await client.view();
  const job=Object.keys(state.cache)[0],doc=state.cache[job].document_comments[0],documentMode=cfg.mode.startsWith('document');
  const parent=documentMode?{parent_type:'estimate',target:doc.id}:{parent_type:'job',target:job};
  const seed=await client.stageComment(job,{...parent,content:'SENSITIVE cached Comment'},[]);await client.submitBatch([seed]);await client.synchronize();
  assert(JSON.stringify(await client.view()).includes('SENSITIVE cached Comment'));
  const committed=await client.stageComment(job,{...parent,content:'SENSITIVE committed lost response'},[]);
  const sibling=await client.stageComment(job,{...parent,content:'SENSITIVE unsubmitted sibling'},[]);
  interrupt=true;await assert.rejects(()=>client.submitBatch([committed]));const before=posts;
  await fetch(cfg.url+(documentMode?'/__fixture/document-loss':'/__fixture/scope-loss'),{method:'POST'});
  client=restart();
  if(cfg.mode.endsWith('failure')){
   const outcome=freshReconcile(committed,job,parent,sibling);assert(outcome.error,'real persistence failure must be reported');assert.equal(outcome.view,'store_busy');assert.equal(outcome.stage,'store_busy');assert.equal(outcome.submit,'store_busy');
   await fs.rm(path.join(directory,'cache'),{recursive:true});await fs.rename(path.join(directory,'cache-held'),path.join(directory,'cache'));
   await fetch(cfg.url+'/__fixture/restore-access',{method:'POST'});
   assert.equal(await freshView(),'store_busy','failed withholding cannot revive old authority in fresh process');
   assert.match(outcome.error,/protected_store_locked/);
   const receipt=await (await fetch(cfg.url+'/comment-operations/'+committed,{headers:{Authorization:'Bearer '+cfg.token}})).json();
   assert.equal(receipt.data.code,'accepted');assert(!JSON.stringify(receipt).includes('SENSITIVE'));
  }else{
   const outcome=freshReconcile(committed,job,parent,sibling),result=outcome.result;assert.equal(outcome.view,'locked_review');assert.equal(outcome.stage,'locked_review');assert.equal(outcome.submit,'locked_review');assert.equal(result.receipt.code,'accepted');assert(!JSON.stringify(result).includes('SENSITIVE'));
   assert.equal(await freshView(),'locked_review','receipt-first reconnect must withhold cached data in fresh process');
   client=restart();await assert.rejects(()=>client.view(),/locked_review/);
   await assert.rejects(()=>client.stageComment(job,{...parent,content:'must not stage'},[]),/locked_review/);
   await assert.rejects(()=>client.submitBatch([sibling]),/locked_review/,'pending submission also requires current synchronization');
   await client.synchronize();state=await client.view();assert.equal(state.pending[sibling].state,'purged');assert(!JSON.stringify(state).includes('SENSITIVE'));
   await fetch(cfg.url+'/__fixture/restore-access',{method:'POST'});await client.synchronize();assert.equal((await client.view()).pending[sibling].state,'purged');
  }
  assert.equal(posts,before,'reconnect never replays');console.log('PASS receipt-first '+cfg.mode);
 } finally {await store.destroyKey();await fs.rm(directory,{recursive:true,force:true});}
})().catch(e=>{console.error(e);process.exitCode=1;});
