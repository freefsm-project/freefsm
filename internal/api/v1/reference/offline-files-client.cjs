'use strict';
const {randomUUID,createHash}=require('node:crypto');
const {OfflineNotes,ProtectedStore}=require('./offline-notes-client.cjs');
const clone=x=>JSON.parse(JSON.stringify(x));

// File intent shares the existing protected store, lease, quarantine, batch and
// receipt lifecycle. This module adds domain selection, not cached authority.
class OfflineFiles extends OfflineNotes {
 receiptPath(e,id){return e.family==='file'?'/file-operations/'+encodeURIComponent(id):super.receiptPath(e,id);}
 pendingTargetVisible(e,records){
  if(!super.pendingTargetVisible(e,records))return false;
  if(e.family!=='file'||!e.file)return true;
  const record=records[e.job],original=e.file;
  if(original.kind==='publish')return (record.file_parents||[]).some(p=>p.object_type===original.object_type&&p.object_id===original.object_id&&p.can_upload);
  return (record.files||[]).some(f=>f.id===original.target&&f.object_type===original.object_type&&f.object_id===original.object_id);
 }
  safeTransport(e){
   if(!e.transport)return undefined;
   const safe=op=>({id:op.id,state:op.state,receipt:op.receipt,retryNotBefore:op.retryNotBefore});
   return {start:safe(e.transport.start),chunks:Object.fromEntries(Object.values(e.transport.chunks).map(op=>[op.id,safe(op)]))};
  }
  purgeSensitiveTarget(s,job){for(const e of Object.values(s.pending))if(e.job===job){e.transport=this.safeTransport(e);delete e.upload;delete e.file;}super.purgeSensitiveTarget(s,job);}
  purgedOperation(e){return {...super.purgedOperation(e),transport:this.safeTransport(e)};}
 receipt(e,id,r){super.receipt(e,id,r);if(e.family==='file'&&e.state==='accepted')delete e.upload;}
 fileParent(s,job,parent){
  const parents=s.cache[job]?.file_parents||[];
  const selected=parent?parents.find(p=>p.object_type===parent.object_type&&p.object_id===String(parent.object_id)):parents.find(p=>p.object_type==='job');
  if(!selected?.can_upload||!Number.isSafeInteger(Number(selected.object_id)))throw Error('review_required');
  return selected;
 }
 stagePublicationIn(s,job,name,bytes,dependencies,parent){
  const selected=this.fileParent(s,job,parent);
  if(typeof name!=='string'||!name||!Buffer.isBuffer(bytes)||!bytes.length)throw Error('review_required');
  const id=randomUUID(),target=randomUUID();
  s.pending[id]={family:'file',job,state:'pending',file:{kind:'publish',target,object_type:selected.object_type,object_id:selected.object_id},
   intent:{operation_id:id,object_type:selected.object_type,object_id:Number(selected.object_id),occurred_at:new Date(this.now()).toISOString()},
   batch:{type:'file.publish',target,depends_on:this.reviewedDependencies(dependencies)},
    upload:{name,bytes:bytes.toString('base64'),size:bytes.length,sha256:createHash('sha256').update(bytes).digest('hex')},
    transport:{start:{id:randomUUID(),state:'pending'},chunks:{}}};
  return id;
 }
 async stageFilePublication(job,name,bytes,dependencies,parent){return this.transaction(async s=>{await this.enforceOfflineAccessWindow(s);const id=this.stagePublicationIn(s,job,name,bytes,dependencies,parent);await this.persist(s);return id;});}
 stageMutationIn(s,job,id,kind,name,dependencies,revision){
  const f=s.cache[job]?.files?.find(f=>f.id===String(id));
  if(!['rename','remove'].includes(kind)||!f?.capabilities?.[kind]||!Number.isSafeInteger(Number(f.object_id))||revision&&revision!==f.revision||kind==='rename'&&typeof name!=='string')throw Error('review_required');
  const operation=randomUUID();s.pending[operation]={family:'file',job,state:'pending',file:{kind,target:f.id,object_type:f.object_type,object_id:f.object_id},
   intent:{operation_id:operation,object_type:f.object_type,object_id:Number(f.object_id),revision:f.revision,...(kind==='rename'?{name}:{}),occurred_at:new Date(this.now()).toISOString()},
   batch:{type:'file.'+kind,target:f.id,depends_on:this.reviewedDependencies(dependencies)}};return operation;
 }
 async stageFileMutation(job,id,kind,name,dependencies){return this.transaction(async s=>{await this.enforceOfflineAccessWindow(s);const operation=this.stageMutationIn(s,job,id,kind,name,dependencies);await this.persist(s);return operation;});}
  transportOperations(e){return e?.transport?[e.transport.start,...Object.values(e.transport.chunks)]:[];}
  transportReceipt(e,op,r){
   if(r?.operation_id!==op.id||!Number.isFinite(Date.parse(r.accepted_at))||!['accepted','not_found','forbidden','file_conflict','invalid_file'].includes(r.code)||(r.code==='accepted'&&e.file&&r.transfer_id!==e.file.target))throw Error('receipt_mismatch');
   op.receipt=clone(r);op.state=r.code==='accepted'?'accepted':'terminal';
   if(op.state==='terminal'&&e.state==='pending')e.state='terminal';
  }
  async reconcileTransportIn(s,id,operation){
   let e=s.pending[id],op=this.transportOperations(e).find(o=>o.id===operation);
   if(!op)throw Error('unknown_operation');
   if(op.retryNotBefore&&this.now()<op.retryNotBefore)throw Error('retry_after_required');
   let r;
   try{r=await this.contact(s,'/file-transfer-operations/'+encodeURIComponent(operation));}
   finally{await this.receiptContactComplete(s);}
   await this.policy(s,true);
   e=s.pending[id];op=this.transportOperations(e).find(o=>o.id===operation);
   if(!op)return {state:e.state,requires_synchronization:true};
   if(r.status===200){
    this.transportReceipt(e,op,r.data);
    if(['forbidden','not_found'].includes(r.data.code)){this.purgeSensitiveTarget(s,e.job);await this.persistAuthorityLoss(s);}
   }else if(r.status===404&&r.error?.code==='not_found'){
    if(op.state==='unknown'&&e.state==='pending')op.state='pending';
   }else{
    if(r.error?.recovery_disposition==='retry')op.retryNotBefore=this.now()+this.retryDelay(r.error);
    await this.persist(s);throw Error('transport_reconciliation_unavailable');
   }
   await this.persist(s);return {state:op.state,receipt:op.receipt,requires_synchronization:true};
  }
  async reconcileFileTransfer(id,operation){return this.transaction(s=>this.reconcileTransportIn(s,id,operation));}
  async transportFailure(s,e,r,operation){
   if(r.status===404){this.purgeSensitiveTarget(s,e.job);await this.persistAuthorityLoss(s);throw Error('inaccessible_target');}
   if(r.error?.recovery_disposition==='retry'||r.status===429||r.status>=500){
    (operation||e.transport).retryNotBefore=this.now()+this.retryDelay(r.error);await this.persist(s);throw Error('retry_after_required');
   }
   if(r.status>=400&&r.status<500){
    if(operation)operation.state='terminal';
    (operation||e.transport).code=r.error?.code||'unknown_error';e.state='terminal';await this.persist(s);throw Error('review_required');
   }
  }
  async transferResponse(s,e,r){
   await this.transportFailure(s,e,r);
   if(![200,201].includes(r.status))throw Error('file_transfer_unavailable');
   const t=r.data,u=e.upload;
   if(!t||t.id!==e.file.target||t.publication_id!==e.intent.operation_id||t.sha256!==u.sha256||t.size!==u.size||t.name!==u.name||t.object_type!==e.intent.object_type||t.object_id!==e.intent.object_id||!Number.isSafeInteger(t.transferred)||t.transferred<0||t.transferred>u.size)throw Error('transfer_mismatch');
   return t;
  }
  async sendTransport(s,e,op,path,method,body){
   if(op.state!=='pending')throw Error('reconciliation_or_review_required');
   if(op.retryNotBefore&&this.now()<op.retryNotBefore)throw Error('retry_after_required');
   op.state='unknown';await this.persist(s);
   const r=await this.contact(s,path,method,body,op.id);
   await this.transportFailure(s,e,r,op);
   const progress=await this.transferResponse(s,e,r);
   if(op.offset!==undefined&&progress.transferred!==op.offset+op.length)throw Error('transfer_mismatch');
   op.state='accepted';await this.persist(s);return progress;
  }
  async transferFile(id){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);await this.policy(s);
  const e=s.pending[id];if(e?.family!=='file'||e.state!=='pending'||!e.upload||e.file?.kind!=='publish'||!this.pendingTargetVisible(e,s.cache))throw Error('review_required');
   const u=e.upload,bytes=Buffer.from(u.bytes,'base64');
   // Installed protected queues recorded UUIDs without dispositions/segment
   // lengths. Recover receipts, but never guess a missing historical segment.
   if(!e.transport){
    e.transport={start:{id:u.start,state:'unknown'},chunks:{}};
    for(const [offset,operation]of Object.entries(u.chunks||{}))e.transport.chunks[offset]={id:operation,state:'unknown',offset:Number(offset)};
    await this.persist(s);
   }
   const unknown=this.transportOperations(e).find(o=>o.state==='unknown');
   if(unknown)return this.reconcileTransportIn(s,id,unknown.id);
   if(this.transportOperations(e).some(o=>o.state==='terminal'))throw Error('review_required');
   if(e.transport.retryNotBefore&&this.now()<e.transport.retryNotBefore)throw Error('retry_after_required');
   const limits=await this.contact(s,'/files/limits');await this.transportFailure(s,e,limits);
   if(limits.status!==200||!Number.isSafeInteger(limits.data?.max_file_bytes)||u.size>limits.data.max_file_bytes)throw Error('review_required');
  const transfer={id:e.file.target,publication_id:id,object_type:e.intent.object_type,object_id:e.intent.object_id,name:u.name,size:u.size,sha256:u.sha256};
   let progress=e.transport.start.state==='accepted'?await this.transferResponse(s,e,await this.contact(s,'/file-transfers/'+transfer.id)):
    await this.sendTransport(s,e,e.transport.start,'/file-transfers','POST',transfer);
  const chunk=limits.data.max_chunk_bytes;if(!Number.isSafeInteger(chunk)||chunk<=0||chunk>1048576)throw Error('file_transfer_unavailable');
  while(progress.transferred<u.size){
   const offset=progress.transferred;if(!Number.isSafeInteger(offset)||offset<0)throw Error('file_transfer_unavailable');
    let op=e.transport.chunks[offset];
    if(!op){op=e.transport.chunks[offset]={id:randomUUID(),state:'pending',offset,length:Math.min(chunk,u.size-offset)};await this.persist(s);}
    if(!Number.isSafeInteger(op.length)||op.length<=0||op.length>chunk){op.state='terminal';op.code='segment_review_required';e.state='terminal';await this.persist(s);throw Error('review_required');}
    progress=await this.sendTransport(s,e,op,'/file-transfers/'+transfer.id+'/chunks?offset='+op.offset,'PUT',bytes.subarray(op.offset,op.offset+op.length));
  }
   const verified=await this.transferResponse(s,e,await this.contact(s,'/file-transfers/'+transfer.id+'/verify','POST'));
  if(!verified.verified||verified.sha256!==u.sha256)throw Error('file_transfer_unavailable');
  u.verified=true;await this.persist(s);return {state:'transferred_not_published',transfer_id:transfer.id};
 });}
 async repairFile(id,review,dependencies){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const e=s.pending[id];
  if(e?.family!=='file'||!['terminal','restore_uncertain'].includes(e.state)||!e.file||!e.job||!this.pendingTargetVisible(e,s.cache))throw Error('review_required');
  if(e.state==='restore_uncertain'&&!e.reconciled)throw Error('restore_reconciliation_required');
  let next;
  if(e.file.kind==='publish'){
   if(!e.upload||review?.object_type!==e.file.object_type||String(review?.object_id)!==e.file.object_id)throw Error('review_required');
    next=this.stagePublicationIn(s,e.job,review.name===undefined?e.upload.name:review.name,Buffer.from(e.upload.bytes,'base64'),dependencies,e.file);
  }else{
   if(!review?.revision)throw Error('review_required');
   next=this.stageMutationIn(s,e.job,e.file.target,e.file.kind,review.name,dependencies,review.revision);
  }
  e.state='replaced';delete e.upload;await this.persist(s);return next;
 });}
}
module.exports={OfflineFiles,ProtectedStore};
