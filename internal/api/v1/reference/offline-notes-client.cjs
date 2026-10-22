'use strict';
const {randomUUID}=require('node:crypto');
const {ProtectedFieldClient,ProtectedStore}=require('./protected-field-client.cjs');
const clone=x=>JSON.parse(JSON.stringify(x));
const jobCategories=new Set(['job:new','job:travel_time','job:in_progress','job:pending','job:completed','job:canceled']);

// Job fields own their target/revision rules. Both Job and Clock workflows use
// the same protected authority, receipt recovery and quarantine implementation.
class OfflineFieldClient extends ProtectedFieldClient {
 async withholdCache(s){
  s.synced=false;s.cache={};s.notes={};s.syncCursor=null;
  if(!this.store.quarantined)await this.persistAuthorityLoss(s);
 }
 async receiptContactComplete(s){await this.withholdCache(s);}
 purgeSensitiveTarget(s,job){
  delete s.cache[job];delete s.notes[job];
  for(const e of Object.values(s.pending))if(e.job===job){
   delete e.intent;delete e.batch;delete e.subtask;delete e.job;delete e.parentType;e.state='purged';
  }
 }
 async synchronize(delta=false){return this.transaction(async s=>{
  if(delta){await this.enforceOfflineAccessWindow(s);if(!s.syncCursor)throw Error('synchronization_incomplete');}
  await this.policy(s);const cursor=delta?s.syncCursor:null;
  const previous=delta?clone(s.cache):{};
  s.synced=false;s.cache={};s.notes={};await this.persist(s);
  const snapshot=await this.pages(s,'/sync/jobs?scope=assigned',cursor,previous),records=snapshot.records;
  for(const e of Object.values(s.pending)){
     if(e.job&&!this.pendingTargetVisible(e,records))this.purgeSensitiveTarget(s,e.job);
   else if(e.family==='comment'&&e.job&&e.batch&&!this.commentProjection(records,e.job,{parent_type:e.intent?.comment?.parent_type||e.parentType,target:e.batch.target})){
    delete e.intent;delete e.batch;delete e.job;delete e.parentType;e.state='purged';
   }
   else if(s.restore&&!['replaced','purged'].includes(e.state))e.state='restore_uncertain';
  }
  // A complete snapshot/delta and its cursor commit together; a failed observed
  // authority-loss write cannot revive old data after authority returns.
  s.cache=records;s.syncCursor=snapshot.cursor;s.synced=true;s.restore=false;await this.persistAuthorityLoss(s);
  return {state:'synchronized',count:Object.keys(records).length};
 });}
  pendingTargetVisible(e,records){return !!records[e.job];}
  async readNotes(job){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);await this.policy(s);
  if(!s.cache[job])throw Error('inaccessible_target');
  const r=await this.contact(s,`/jobs/${encodeURIComponent(job)}/field-notes`);
  if(r.status===404){this.purgeSensitiveTarget(s,job);await this.persistAuthorityLoss(s);throw Error('inaccessible_target');}
  if(r.status!==200||typeof r.data?.notes_revision!=='string'||!r.data.notes_revision)throw Error('notes_unavailable');
  s.notes[job]=r.data;await this.persist(s);return clone(r.data);
 });}
 async view(){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const pending=clone(s.pending);
  const labels={pending:'Pending — not accepted',unknown:'Save status unknown — check original receipt',accepted:'Accepted by server',terminal:'Needs attention',restore_uncertain:'Needs attention — restore outcome uncertain',purged:'Inaccessible content purged',replaced:'Replaced by explicitly reviewed intent'};
  for(const e of Object.values(pending))e.label=labels[e.state]||'Review required';
  return clone({cache:s.cache,notes:s.notes,pending});
 });}
 stageIn(s,job,text){
  if(!s.cache[job]||!s.notes[job]?.can_edit_notes||typeof text!=='string')throw Error('review_required');
  const id=randomUUID();s.pending[id]={job,state:'pending',intent:{operation_id:id,notes_revision:s.notes[job].notes_revision,field_notes:text,occurred_at:new Date(this.now()).toISOString()}};return id;
 }
 async stage(job,text){return this.transaction(async s=>{await this.enforceOfflineAccessWindow(s);const id=this.stageIn(s,job,text);await this.persist(s);return id;});}
  async stageBatch(job,text,dependencies){return this.transaction(async s=>{
   await this.enforceOfflineAccessWindow(s);const depends_on=this.reviewedDependencies(dependencies);
   const id=this.stageIn(s,job,text);s.pending[id].batch={type:'job.field_notes',target:job,depends_on};await this.persist(s);return id;
   });}
  commentProjection(cache,job,reviewed){
   const record=cache[job];if(!record)return null;
   if(reviewed.parent_type==='job'&&reviewed.target===job)return record.comments;
   return record.document_comments?.find(d=>d.parent_type===reviewed.parent_type&&d.id===reviewed.target)?.comments||null;
  }
  stageCommentIn(s,job,reviewed,dependencies){
   if(!reviewed||typeof reviewed!=='object'||Array.isArray(reviewed)||Object.keys(reviewed).some(k=>!['parent_type','target','content','reply_to','remove_id','parent_revision'].includes(k)))throw Error('review_required');
   const projection=this.commentProjection(s.cache,job,reviewed);
   if(!projection||typeof projection.eligibility_revision!=='string'||!projection.eligibility_revision||(Object.hasOwn(reviewed,'parent_revision')&&reviewed.parent_revision!==projection.eligibility_revision))throw Error('review_required');
   const removal=Object.hasOwn(reviewed,'remove_id');
   if(removal){
    if(typeof reviewed.remove_id!=='string'||Object.hasOwn(reviewed,'content')||Object.hasOwn(reviewed,'reply_to')||!projection.comments.some(c=>c.id===reviewed.remove_id&&c.can_remove))throw Error('review_required');
   }else{
    if(!projection.can_add||typeof reviewed.content!=='string'||!reviewed.content.trim()||reviewed.content.includes('\0')||[...reviewed.content].length>10000)throw Error('review_required');
    if(Object.hasOwn(reviewed,'reply_to')&&(typeof reviewed.reply_to!=='string'||!projection.comments.some(c=>c.id===reviewed.reply_to&&c.can_reply)))throw Error('review_required');
   }
   const depends_on=this.reviewedDependencies(dependencies),id=randomUUID();
   const {target,...comment}=clone(reviewed);
   comment.parent_revision=projection.eligibility_revision;
   s.pending[id]={job,family:'comment',parentType:comment.parent_type,state:'pending',intent:{operation_id:id,occurred_at:new Date(this.now()).toISOString(),comment},batch:{type:removal?'comment.remove':'comment.add',target,depends_on}};
   return id;
  }
  async stageComment(job,reviewed,dependencies){return this.transaction(async s=>{
   await this.enforceOfflineAccessWindow(s);const id=this.stageCommentIn(s,job,reviewed,dependencies);await this.persist(s);return id;
  });}
  async repairComment(id,reviewed,dependencies){return this.transaction(async s=>{
   await this.enforceOfflineAccessWindow(s);const e=s.pending[id];
   if(!e||e.family!=='comment'||!['terminal','restore_uncertain'].includes(e.state)||!e.intent||!e.job||!e.batch)throw Error('review_required');
   if(e.state==='restore_uncertain'&&!e.reconciled)throw Error('restore_reconciliation_required');
   const old=e.intent.comment;
   if(!reviewed?.parent_revision||reviewed.target!==e.batch.target||reviewed.parent_type!==old.parent_type||reviewed.reply_to!==old.reply_to||reviewed.remove_id!==old.remove_id)throw Error('review_required');
   const next=this.stageCommentIn(s,e.job,reviewed,dependencies);e.state='replaced';await this.persist(s);return next;
  });}
  stageFieldsIn(s,job,values,dependencies){
   const fields=s.cache[job]?.custom_fields;
   if(!fields?.can_edit||!Array.isArray(values)||!values.length)throw Error('review_required');
   const revisions={},seen=new Set();
   for(const value of values){
    const definition=fields.definitions.find(d=>d.id===value.definition_id);
    if(!definition||definition.retired||seen.has(definition.id)||!fields.revisions[String(definition.id)])throw Error('review_required');
    seen.add(definition.id);revisions[String(definition.id)]=fields.revisions[String(definition.id)];
   }
   const depends_on=this.reviewedDependencies(dependencies),id=randomUUID();
   s.pending[id]={job,state:'pending',receipt_family:'job-field',intent:{operation_id:id,revisions,values:clone(values),occurred_at:new Date(this.now()).toISOString()},batch:{type:'job.fields',target:job,depends_on}};
   return id;
  }
  async stageFields(job,values,dependencies){return this.transaction(async s=>{
   await this.enforceOfflineAccessWindow(s);const id=this.stageFieldsIn(s,job,values,dependencies);await this.persist(s);return id;
  });}
  async repairFields(id,values,reviewedRevisions,dependencies){return this.transaction(async s=>{
   await this.enforceOfflineAccessWindow(s);const e=this.repairableField(s,id,'job.fields');
   if(!Array.isArray(values))throw Error('review_required');
   const fields=s.cache[e.job]?.custom_fields;
   if(!fields||!reviewedRevisions||values.some(v=>!reviewedRevisions[String(v.definition_id)]||reviewedRevisions[String(v.definition_id)]!==fields.revisions[String(v.definition_id)]))throw Error('review_required');
   const next=this.stageFieldsIn(s,e.job,values,dependencies);e.state='replaced';await this.persist(s);return next;
  });}
  stageStatusIn(s,job,status,revision,meaning,dependencies){
   const record=s.cache[job],definition=record?.status_definitions?.find(v=>v.id===status);
   if(record?.field_capabilities?.status!==true||!definition||!jobCategories.has(definition.category)||!revision||!meaning||record.status_revision!==revision||definition.meaning_revision!==meaning)throw Error('review_required');
   const id=randomUUID();s.pending[id]={job,family:'job.execution',state:'pending',intent:{operation_id:id,status_id:status,status_revision:revision,meaning_revision:meaning,occurred_at:new Date(this.now()).toISOString()},batch:{type:'job.status',target:job,depends_on:this.reviewedDependencies(dependencies)}};return id;
  }
  stageSubtaskIn(s,job,subtask,completed,revision,dependencies){
   const record=s.cache[job],task=record?.subtasks?.find(v=>v.id===subtask);
   if(typeof completed!=='boolean'||!task||typeof task.completed!=='boolean'||!revision||record.field_capabilities?.[completed?'subtask_complete':'subtask_reopen']!==true||task.completion_revision!==revision)throw Error('review_required');
   const id=randomUUID();s.pending[id]={job,subtask,family:'job.subtask',state:'pending',intent:{operation_id:id,subtask_id:subtask,completed,completion_revision:revision,occurred_at:new Date(this.now()).toISOString()},batch:{type:'job.subtask',target:job,depends_on:this.reviewedDependencies(dependencies)}};return id;
  }
  async stageStatus(job,status,revision,meaning,dependencies){return this.transaction(async s=>{await this.enforceOfflineAccessWindow(s);const id=this.stageStatusIn(s,job,status,revision,meaning,dependencies);await this.persist(s);return id;});}
  async stageSubtask(job,subtask,completed,revision,dependencies){return this.transaction(async s=>{await this.enforceOfflineAccessWindow(s);const id=this.stageSubtaskIn(s,job,subtask,completed,revision,dependencies);await this.persist(s);return id;});}
  repairableField(s,id,type){
   const e=s.pending[id];
   if(!e||e.batch?.type!==type||!e.job||!['terminal','restore_uncertain'].includes(e.state))throw Error('review_required');
   if(e.state==='restore_uncertain'&&!e.reconciled)throw Error('restore_reconciliation_required');
   return e;
  }
  async repairStatus(id,status,revision,meaning,dependencies){return this.transaction(async s=>{
   await this.enforceOfflineAccessWindow(s);const e=this.repairableField(s,id,'job.status');
   const next=this.stageStatusIn(s,e.job,status,revision,meaning,dependencies);e.state='replaced';await this.persist(s);return next;
  });}
  async repairSubtask(id,completed,revision,dependencies){return this.transaction(async s=>{
   await this.enforceOfflineAccessWindow(s);const e=this.repairableField(s,id,'job.subtask');
   const next=this.stageSubtaskIn(s,e.job,e.subtask,completed,revision,dependencies);e.state='replaced';await this.persist(s);return next;
  });}
 async submit(id){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const e=s.pending[id];
  if(!e||e.state!=='pending'||!e.intent||e.batch)throw Error('reconciliation_or_review_required');
  if(e.retryNotBefore&&this.now()<e.retryNotBefore)throw Error('retry_after_required');
  await this.policy(s);
  e.state='unknown';await this.persist(s);
  const r=await this.contact(s,`/jobs/${encodeURIComponent(e.job)}/field-notes`,'POST',e.intent);
  if(r.status===200){this.receipt(e,id,r.data);}
  else if([400,404,409].includes(r.status)){e.state='terminal';e.code=r.error?.code;if(r.status===404)this.purgeSensitiveTarget(s,e.job);}
  else if(r.status===429||r.status>=500)e.retryNotBefore=this.now()+this.retryDelay(r.error);
  if(r.status===404)await this.persistAuthorityLoss(s);else await this.persist(s);
  return {state:e.state,code:e.code,retry_not_before:e.retryNotBefore};
 });}
 receiptPath(e,id){return `/${e.family==='comment'?'comment-operations':e.family==='job.subtask'?'subtask-operations':e.receipt_family==='job-field'?'job-field-operations':'job-execution-operations'}/${encodeURIComponent(id)}`;}
 async receiptAuthority(s,e,code,receiptOnly=false){
  if(e.family==='comment'&&code!=='accepted')await this.withholdCache(s);
  if(!['forbidden','not_found'].includes(code))return false;
  if(receiptOnly)await this.purge(s);else this.purgeSensitiveTarget(s,e.job);
  return true;
 }
 async repair(id,text,reviewedRevision,reviewedDependencies){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const e=s.pending[id];
   if(!e||e.family==='comment'||!['terminal','restore_uncertain'].includes(e.state)||!e.job||(e.batch&&e.batch.type!=='job.field_notes'))throw Error('review_required');
  if(e.state==='restore_uncertain'&&!e.reconciled)throw Error('restore_reconciliation_required');
  if(!reviewedRevision||s.notes[e.job]?.notes_revision!==reviewedRevision)throw Error('review_required');
  const batch=e.batch?{type:e.batch.type,target:e.batch.target,depends_on:this.reviewedDependencies(reviewedDependencies)}:null;
  const next=this.stageIn(s,e.job,text);if(batch)s.pending[next].batch=batch;e.state='replaced';await this.persist(s);return next;
 });}
}
module.exports={OfflineFieldClient,OfflineNotes:OfflineFieldClient,ProtectedStore};
