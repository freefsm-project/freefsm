'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const {randomUUID}=require('node:crypto');
const {spawnSync}=require('node:child_process');
const {PDFClient,ProtectedStore}=require('../reference/pdf-client.cjs');
const {CommercialClient}=require('../reference/commercial-client.cjs');
(async()=>{
 const input=JSON.parse(fs.readFileSync(0,'utf8'));
 const store=new ProtectedStore(input.directory,input.key||randomUUID());
 let offline=false,lose=false,posts=0,readFault=null;
 const transport=async(path,method='GET',body,auth)=>{
  assert(!offline,'offline staging contacted server');
  if(path.endsWith('/pdf/generate')){
   posts++;assert.equal((await store.load()).pending[body.operation_id].state,'unknown');
  }
  const headers={Authorization:'Bearer '+auth.token};
  if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;
  if(auth.operation)headers['Operation-ID']=auth.operation;
  if(body!==undefined)headers['Content-Type']='application/json';
  // Fault seam: a missing exact-version HTTP response is ambiguous about the
  // parent; malformed/gateway/transport failures must not erase pending intent.
  const pdfRead=readFault&&path.includes('/pdf/versions');
  const requestPath=pdfRead?path.replace(/\/pdf\/versions.*$/,'/pdf/versions/999999999'):path;
  const r=await fetch(input.url+requestPath,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
  const result=r.headers.get('content-type')?.startsWith('application/pdf')?{status:r.status,bytes:Buffer.from(await r.arrayBuffer())}:{status:r.status,...await r.json()};
  if(lose&&path.endsWith('/pdf/generate')){lose=false;throw Error('lost_response');}
  if(pdfRead&&readFault==='malformed')return {status:404,error:{code:'not_found'}};
  if(pdfRead&&readFault==='transport')throw Error('transport_failed');
  return result;
 };
 let pdf=new PDFClient(store,transport);
  if(input.restart){
   if(input.lock)await assert.rejects(pdf.view(),{message:input.lock});
   if(input.lock!=='store_busy'){assert.equal((await pdf.reconcile(input.id)).receipt.outcome,'accepted');assert.equal(posts,0);}
   return;
  }
 const control=action=>fetch(input.url+'/__fixture/'+action,{method:'POST',headers:{Authorization:'Bearer '+input.token}});
 try{
  await pdf.initialize();await pdf.authenticate(input.token);await pdf.synchronize(input.kind);
  const target=Object.keys((await pdf.view()).documents)[0];
  const review=await pdf.reviewSource(target);
  offline=true;
  const id=await pdf.stagePDF(target,review.source_token);
  const original=(await pdf.view()).pending[id].intent;
  assert.equal(posts,0);assert.equal((await pdf.view()).pending[id].state,'pending');
  for(const name of fs.readdirSync(input.directory))assert(!fs.readFileSync(input.directory+'/'+name).includes(Buffer.from('ENCRYPTED-PDF-REVIEW')));
  offline=false;
  const commercial=new CommercialClient(store,transport);
  await commercial.synchronize(input.kind);
  assert.deepEqual((await pdf.view()).pending[id].intent,original,'commercial sync purged supported PDF intent');
  if(input.scenario==='scope_loss'){
   await control('scope-loss');await pdf.synchronize(input.kind);
   const view=await pdf.view();assert(!view.pending[id].intent);assert(!view.pending[id].reviewed);assert.deepEqual(view.documents,{});assert.equal(posts,0);return;
  }
  if(input.scenario==='retry'||input.scenario==='drift'){
   if(input.scenario==='retry')await control('fault');else await control('drift');
   const result=await pdf.submit(id);assert.equal(result.status,input.scenario==='retry'?503:409);
   assert.equal((await pdf.reconcile(id)).state,input.scenario==='retry'?'unreceived':'terminal');
   assert.equal(posts,1);
   await pdf.synchronize(input.kind);
   const comparison=await pdf.compare(id);assert.deepEqual(comparison.proposed,original);
   if(input.scenario==='retry'){
    assert.equal(comparison.same_source,true);
    await assert.rejects(pdf.submit(id),{message:'reconciliation_or_review_required'});
    await assert.rejects(pdf.retryUnreceived(id),{message:'review_required'});
    await control('repair');pdf.now=()=>Date.now()+6000;
    assert.equal((await pdf.retryUnreceived(id,{confirm:true})).receipt.outcome,'accepted');
   }else{
    assert.equal(comparison.same_source,false);
    const next=await pdf.repair(id,comparison.current.source_token);assert.notEqual(next,id);
    assert.equal((await pdf.submit(next)).receipt.outcome,'accepted');
   }
   assert.equal(posts,2);return;
  }
  if(input.scenario==='checkpoint'){
   const save=store.save.bind(store);store.save=async s=>{if(s.pending[id].state==='unknown')throw Error('checkpoint_failed');return save(s);};
   await assert.rejects(pdf.submit(id),{message:'protected_store_locked'});assert.equal(posts,0);
   await assert.rejects(new PDFClient(new ProtectedStore(input.directory,store.id),transport).view(),{message:'store_busy'});
   store.save=save;store.quarantined=false;fs.unlinkSync(input.directory+'/.writer');return;
  }
  if(input.scenario==='conversion'||input.scenario==='reversion'){
   const doc=(await commercial.view()).documents[target];
   const conversion=await commercial.stageConversion(target,doc.revision,input.kind==='invoice'?{cycle_id:doc.cycle_id}:{});
   assert.equal((await commercial.submit(conversion)).receipt.outcome,'accepted');
   const pending=(await store.load()).pending[id];assert.equal(pending.state,'purged');assert(!pending.intent);assert(!pending.reviewed);
   assert.deepEqual((await store.load()).pdfSources,{});assert.equal(posts,0);return;
  }
  if(input.scenario==='expiry'){
   pdf.now=()=>Date.now()+73*3600000;
   await assert.rejects(pdf.view(),{message:'authorization_lease_expired'});
   assert.equal((await pdf.reconcile(id)).state,'pending');assert.equal(posts,0);return;
  }
  if(input.scenario==='acceptance_checkpoint'){
   const save=store.save.bind(store);store.save=async s=>{if(s.pending[id].state==='accepted')throw Error('checkpoint_failed');return save(s);};
   await assert.rejects(pdf.submit(id),{message:'checkpoint_failed'});assert.equal(posts,1);
   pdf=new PDFClient(new ProtectedStore(input.directory,store.id),transport);
   assert.equal((await pdf.reconcile(id)).receipt.outcome,'accepted');assert.equal(posts,1);return;
  }
  lose=true;await assert.rejects(pdf.submit(id),{message:'lost_response'});
  pdf=new PDFClient(new ProtectedStore(input.directory,store.id),transport);
  if(input.scenario==='lifecycle'){
   const child=spawnSync(process.execPath,[__filename],{input:JSON.stringify({...input,key:store.id,id,restart:true}),encoding:'utf8',timeout:30000});
   assert.equal(child.status,0,child.stdout+child.stderr);
  }
  if(input.scenario==='scope_receipt'){
   await control('scope-loss');pdf.now=()=>Date.now()+73*3600000;
   const receipt=(await pdf.reconcile(id)).receipt;assert.equal(receipt.outcome,'accepted');assert(receipt.version_id>0);
   const state=await store.load();assert(!state.pending[id].reviewed);assert(!state.pending[id].intent);assert.deepEqual(state.documents,{});assert.equal(posts,1);return;
  }
  if(input.scenario==='restore'){
   const fresh=await (await control('restore')).json();
   await assert.rejects(pdf.reconcile(id),{message:'locked_review'});
   await pdf.authenticate(fresh.token);await pdf.synchronize(input.kind);
   assert.equal((await pdf.reconcile(id)).state,'restore_uncertain');assert.equal(posts,1);
   await pdf.synchronize(input.kind);await pdf.acknowledgeUncertain(id);
   const comparison=await pdf.compare(id);
   await assert.rejects(pdf.retryUnreceived(id,{confirm:true}),{message:'review_required'});
   const next=await pdf.repair(id,comparison.current.source_token);assert.notEqual(next,id);
   assert.equal((await pdf.submit(next)).receipt.outcome,'accepted');assert.equal(posts,2);return;
  }
  if(input.scenario==='revoke'){
   await control('revoke');await assert.rejects(pdf.reconcile(id),{message:'authorization_purged'});
   const state=await store.load();assert(!state.pending[id].intent);assert(!state.pending[id].reviewed);assert.deepEqual(state.pdfSources,{});assert.equal(posts,1);return;
  }
  assert.equal((await pdf.reconcile(id)).receipt.outcome,'accepted');
  assert.equal(posts,1,'receipt reconciliation generated another PDF');
  await pdf.synchronize(input.kind);
  const accepted=(await pdf.view()).pending[id].receipt;
  assert.equal((await pdf.acceptedVersion(id)).version_id,accepted.version_id);
  if(input.scenario.startsWith('read_')){
   const current=await pdf.reviewSource(target),pending=await pdf.stagePDF(target,current.source_token);
   await pdf.versions(target);await pdf.selectVersion(target,accepted.version_id);
   assert((await pdf.view()).sources[target].source);
   assert((await pdf.view()).pending[pending].reviewed.source);
   const lost=input.scenario.startsWith('read_loss_');
   if(lost)await control('scope-loss');else readFault=input.scenario.split('_')[1];
   const failStorage=input.scenario.endsWith('_storage'),save=pdf.store.save.bind(pdf.store);
   if(failStorage)pdf.store.save=async s=>{
    const fail=input.scenario.includes('_purge_storage')?s.pending[pending].state==='purged':!s.synced||!s.documents?.[target];
    if(fail)throw Error('purge_checkpoint_failed');return save(s);
   };
   try{
    const action=()=>input.scenario.includes('_versions')?pdf.versions(target):pdf.download(target);
    await assert.rejects(action(),{message:failStorage?'protected_store_locked':readFault==='transport'?'transport_failed':'not_found'});
    const lock=failStorage?'store_busy':'locked_review';
    await assert.rejects(pdf.view(),{message:lock});
    const child=spawnSync(process.execPath,[__filename],{input:JSON.stringify({...input,key:store.id,id,restart:true,lock}),encoding:'utf8',timeout:30000});
    assert.equal(child.status,0,child.stdout+child.stderr);
    if(!failStorage){
     const state=await store.load();assert.deepEqual(state.documents,{});assert.deepEqual(state.pdfSources,{});assert.deepEqual(state.pdfSelections,{});
     if(lost){assert.deepEqual(state.pending[pending],{family:'pdf',state:'purged'});assert(!state.pending[id].target);}
     else{
      assert.equal(state.pending[pending].intent.source_token,current.source_token);
      readFault=null;await pdf.synchronize(input.kind);
      assert.equal((await pdf.view()).pending[pending].state,'pending','ambiguous failure destroyed recoverable intent');
     }
     assert.equal((await pdf.reconcile(id)).receipt.version_id,accepted.version_id);
    }
    assert.equal(posts,1);
   }finally{
    // Test-only cleanup after proving the durable quarantine blocks restart.
    if(failStorage){pdf.store.save=save;pdf.store.quarantined=false;if(fs.existsSync(input.directory+'/.writer'))fs.unlinkSync(input.directory+'/.writer');}
   }
   return;
  }
  if(input.scenario==='archived'){
   const doc=(await pdf.view()).documents[target];await control('archive');
   pdf.now=()=>Date.now()+73*3600000;
   const retained=await pdf.retainedVersions(input.kind,doc.legacy_id);assert(retained.versions.some(v=>v.id===accepted.version_id));
   let download=await pdf.downloadRetained(input.kind,doc.legacy_id,accepted.version_id);
   if(download.code==='stale_pdf')download=await pdf.downloadRetained(input.kind,doc.legacy_id,accepted.version_id,{confirmVersion:accepted.version_id});
   assert(download.bytes.subarray(0,4).equals(Buffer.from('%PDF')));assert.equal(posts,1);return;
  }
  if(input.scenario==='download_only'){
   await control('download-only');await pdf.synchronize(input.kind);
   const versions=await pdf.versions(target);await pdf.selectVersion(target,versions.default_version);
   const download=await pdf.download(target);assert(download.bytes.subarray(0,4).equals(Buffer.from('%PDF')));
   assert.equal(posts,1);return;
  }
  if(input.scenario==='exact'){
   const versions=await pdf.versions(target);assert.equal(versions.default_version,accepted.version_id);
   await pdf.selectVersion(target,accepted.version_id);
   await control('drift');
   const warning=await pdf.download(target);assert.equal(warning.code,'stale_pdf');assert.equal(warning.version_id,accepted.version_id);
   const newReview=await pdf.reviewSource(target),next=await pdf.stagePDF(target,newReview.source_token);
   const second=await pdf.submit(next);assert.notEqual(second.receipt.version_id,accepted.version_id);
   const refreshed=await pdf.versions(target);assert.equal(refreshed.default_version,second.receipt.version_id);assert.equal(refreshed.selected,accepted.version_id);
   await pdf.synchronize(input.kind);await pdf.versions(target);
   // Reconnection must retain the exact explicit choice, not the new default.
   const again=await pdf.download(target);assert.equal(again.version_id,accepted.version_id);assert.equal(again.code,'stale_pdf');
   const download=await pdf.download(target,{confirmVersion:accepted.version_id});
   assert.equal(download.version_id,accepted.version_id);assert(download.bytes.subarray(0,4).equals(Buffer.from('%PDF')));
   assert.equal(posts,2);assert(!fs.readFileSync(input.directory+'/cache').includes(download.bytes));
  }
  console.log('protected PDF staging, shared synchronization and lost-response restart passed');
 }finally{await pdf.logout();assert(!fs.existsSync(input.directory+'/cache'));assert.throws(()=>store.key(),{message:'key_protection_unavailable'});}
})().catch(error=>{console.error(error);process.exitCode=1;});
