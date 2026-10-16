'use strict';
const {randomUUID}=require('node:crypto');
const {ProtectedFieldClient,ProtectedStore}=require('./protected-field-client.cjs');
const clone=x=>JSON.parse(JSON.stringify(x));

// Notes owns its target/revision workflow; protected authority and receipt-first
// reconciliation are shared with the independent Clock workflow.
class OfflineNotes extends ProtectedFieldClient {
 purgeSensitiveTarget(s,job){
  delete s.cache[job];delete s.notes[job];
  for(const e of Object.values(s.pending))if(e.job===job){
   delete e.intent;delete e.batch;delete e.job;e.state='purged';
  }
 }
 async synchronize(){return this.transaction(async s=>{
  await this.policy(s);s.synced=false;s.cache={};s.notes={};await this.persist(s);
  let page, records={};const seen=new Set();
  do{
   const r=await this.contact(s,'/sync/jobs?scope=assigned&page_size=200'+(page?'&page='+encodeURIComponent(page):''));
   if(r.status!==200||!Array.isArray(r.data?.records)||typeof r.data.complete!=='boolean'||!['none','all'].includes(r.data.purge))throw Error('synchronization_incomplete');
   if(r.data.purge==='all'){await this.purge(s);throw Error('authorization_purged');}
   for(const id of r.data.removed||[])delete records[id];
   for(const record of r.data.records){if(typeof record.id!=='string')throw Error('synchronization_incomplete');records[record.id]=record;}
   if(r.data.complete){if(typeof r.data.completion_cursor!=='string'||!r.data.completion_cursor||r.data.next_page!==null)throw Error('synchronization_incomplete');page=null;}
   else {page=r.data.next_page;if(!page||seen.has(page))throw Error('synchronization_incomplete');seen.add(page);}
  }while(page);
  for(const e of Object.values(s.pending)){
   if(e.job&&!records[e.job])this.purgeSensitiveTarget(s,e.job);
   else if(s.restore&&!['replaced','purged'].includes(e.state))e.state='restore_uncertain';
  }
  // The complete snapshot carries observed scope removals and restore outcomes.
  // Failed persistence must not revive older intent after authority returns.
  s.cache=records;s.synced=true;s.restore=false;await this.persistAuthorityLoss(s);
  return {state:'synchronized',count:Object.keys(records).length};
 });}
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
 async submit(id){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const e=s.pending[id];
  if(!e||e.state!=='pending'||!e.intent||e.batch)throw Error('reconciliation_or_review_required');
  if(e.retryNotBefore&&this.now()<e.retryNotBefore)throw Error('retry_after_required');
  await this.policy(s);
  // Crash/response loss after this durable write always requires receipt lookup.
  e.state='unknown';await this.persist(s);
  const r=await this.contact(s,`/jobs/${encodeURIComponent(e.job)}/field-notes`,'POST',e.intent);
  if(r.status===200){this.receipt(e,id,r.data);}
  else if([400,404,409].includes(r.status)){e.state='terminal';e.code=r.error?.code;if(r.status===404)this.purgeSensitiveTarget(s,e.job);}
  else if(r.status===429||r.status>=500)e.retryNotBefore=this.now()+this.retryDelay(r.error);
  // Even a service failure remains unknown until receipt lookup. Retry is explicit.
  if(r.status===404)await this.persistAuthorityLoss(s);else await this.persist(s);
  return {state:e.state,code:e.code,retry_not_before:e.retryNotBefore};
 });}
 receiptPath(e,id){return `/job-execution-operations/${encodeURIComponent(id)}`;}
 async receiptAuthority(s,e,code,receiptOnly=false){
  if(!['forbidden','not_found'].includes(code))return false;
  if(receiptOnly)await this.purge(s);else this.purgeSensitiveTarget(s,e.job);
  return true;
 }
 async repair(id,text,reviewedRevision,reviewedDependencies){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const e=s.pending[id];
  if(!e||!['terminal','restore_uncertain'].includes(e.state)||!e.job)throw Error('review_required');
  if(e.state==='restore_uncertain'&&!e.reconciled)throw Error('restore_reconciliation_required');
  if(!reviewedRevision||s.notes[e.job]?.notes_revision!==reviewedRevision)throw Error('review_required');
  const batch=e.batch?{type:e.batch.type,target:e.batch.target,depends_on:this.reviewedDependencies(reviewedDependencies)}:null;
  const next=this.stageIn(s,e.job,text);if(batch)s.pending[next].batch=batch;e.state='replaced';await this.persist(s);return next;
 });}
}
module.exports={OfflineNotes,ProtectedStore};
