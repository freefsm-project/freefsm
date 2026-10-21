'use strict';
const {randomUUID,createHash}=require('node:crypto');
const {OfflineNotes,ProtectedStore}=require('./offline-notes-client.cjs');

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
 purgeSensitiveTarget(s,job){for(const e of Object.values(s.pending))if(e.job===job){delete e.upload;delete e.file;}super.purgeSensitiveTarget(s,job);}
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
   upload:{name,bytes:bytes.toString('base64'),size:bytes.length,sha256:createHash('sha256').update(bytes).digest('hex'),start:randomUUID(),chunks:{}}};
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
 async transferFile(id){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);await this.policy(s);
  const e=s.pending[id];if(e?.family!=='file'||e.state!=='pending'||!e.upload||e.file?.kind!=='publish'||!this.pendingTargetVisible(e,s.cache))throw Error('review_required');
  const u=e.upload,bytes=Buffer.from(u.bytes,'base64');
  const limits=await this.contact(s,'/files/limits');if(limits.status!==200||!Number.isSafeInteger(limits.data?.max_file_bytes)||u.size>limits.data.max_file_bytes)throw Error('review_required');
  const transfer={id:e.file.target,publication_id:id,object_type:e.intent.object_type,object_id:e.intent.object_id,name:u.name,size:u.size,sha256:u.sha256};
  const checked=async r=>{if(r.status===404){this.purgeSensitiveTarget(s,e.job);await this.persistAuthorityLoss(s);throw Error('inaccessible_target');}if(![200,201].includes(r.status))throw Error('file_transfer_unavailable');return r.data;};
  let progress=await checked(await this.contact(s,'/file-transfers','POST',transfer,u.start));
  if(progress.id!==transfer.id||progress.sha256!==transfer.sha256||progress.size!==transfer.size||progress.object_type!==transfer.object_type||progress.object_id!==transfer.object_id)throw Error('file_transfer_unavailable');
  const chunk=limits.data.max_chunk_bytes;if(!Number.isSafeInteger(chunk)||chunk<=0||chunk>1048576)throw Error('file_transfer_unavailable');
  while(progress.transferred<u.size){
   const offset=progress.transferred;if(!Number.isSafeInteger(offset)||offset<0)throw Error('file_transfer_unavailable');
   if(!u.chunks[offset]){u.chunks[offset]=randomUUID();await this.persist(s);}
   const next=await checked(await this.contact(s,'/file-transfers/'+transfer.id+'/chunks?offset='+offset,'PUT',bytes.subarray(offset,offset+chunk),u.chunks[offset]));
   if(next.id!==transfer.id||next.transferred<=offset||next.transferred>u.size)throw Error('file_transfer_unavailable');progress=next;
  }
  const verified=await checked(await this.contact(s,'/file-transfers/'+transfer.id+'/verify','POST'));
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
   next=this.stagePublicationIn(s,e.job,e.upload.name,Buffer.from(e.upload.bytes,'base64'),dependencies,e.file);
  }else{
   if(!review?.revision)throw Error('review_required');
   next=this.stageMutationIn(s,e.job,e.file.target,e.file.kind,review.name,dependencies,review.revision);
  }
  e.state='replaced';delete e.upload;await this.persist(s);return next;
 });}
}
module.exports={OfflineFiles,ProtectedStore};
