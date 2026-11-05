'use strict';
const {randomUUID}=require('node:crypto');
const {FieldLeaseClient,ProtectedStore}=require('./lease-indicator-client.cjs');
const clone=x=>JSON.parse(JSON.stringify(x));

// Executable API reference, sharing the established encrypted store, authority
// lifecycle, explicit batch submission and observation invalidation rules.
class ServiceEntryClient extends FieldLeaseClient {
 async receiptAuthority(s,e,code,receiptOnly=false){
  // A relationship conflict is not proof of continuing visibility: unlink,
  // transfer and archive share this outcome with stale revisions. Withhold the
  // whole cached snapshot (including every coverage observation alias) until a
  // fresh authorized snapshot can purge inaccessible targets. Pending identities
  // and original intent stay locked, not rewritten or automatically repaired.
  if(e.family==='service_entry'&&['service_entry_conflict','failed_dependency'].includes(code)){
   await this.withholdCache(s);return true;
  }
  return super.receiptAuthority(s,e,code,receiptOnly);
 }
 projection(cache,job,asset){return cache[job]?.masters?.assets?.find(a=>a.id===asset);}
 pendingTargetVisible(e,records){
  if(e.family!=='service_entry')return super.pendingTargetVisible(e,records);
  const asset=this.projection(records,e.job,e.batch?.asset_id);
  if(!asset?.service_entry_context||asset.service_entry_context.link_id!==e.intent?.link_id&&e.intent)return false;
  if(e.batch.type==='service_entry.revise')return asset.service_entries?.some(row=>row.id===e.intent?.entry_id&&row.can_revise)||e.state==='accepted';
  return asset.service_entry_context.can_create;
 }
 stageServiceEntryIn(s,job,asset,reviewed,dependencies,occurrence){
  if(!reviewed||typeof reviewed!=='object'||Array.isArray(reviewed)||Object.keys(reviewed).some(k=>!['entry_id','revision','service_date','summary','responsible_user_ids'].includes(k)))throw Error('review_required');
  const projection=this.projection(s.cache,job,asset),context=projection?.service_entry_context;
  if(!context||typeof reviewed.service_date!=='string'||typeof reviewed.summary!=='string'||!reviewed.summary.trim()||!Array.isArray(reviewed.responsible_user_ids)||!reviewed.responsible_user_ids.length)throw Error('review_required');
  const revise=Object.hasOwn(reviewed,'entry_id');
  if(revise){
   const entry=projection.service_entries?.find(e=>e.id===reviewed.entry_id);
   if(!entry?.can_revise||entry.revision!==reviewed.revision||entry.link_id!==context.link_id)throw Error('review_required');
  }else if(!context.can_create||Object.hasOwn(reviewed,'revision'))throw Error('review_required');
  const id=randomUUID();
  s.pending[id]={job,family:'service_entry',state:'pending',intent:{...clone(reviewed),operation_id:id,link_id:context.link_id,occurred_at:occurrence||new Date(this.now()).toISOString()},batch:{type:revise?'service_entry.revise':'service_entry.create',job_id:job,asset_id:asset,depends_on:this.reviewedDependencies(dependencies)}};
  return id;
 }
 async stageServiceEntry(job,asset,reviewed,dependencies){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);
  const id=this.stageServiceEntryIn(s,job,asset,reviewed,dependencies);await this.persist(s);return id;
 });}
 async repairServiceEntry(id,reviewed,dependencies){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);
  const e=s.pending[id];
  if(!e||e.family!=='service_entry'||!['terminal','restore_uncertain'].includes(e.state)||!e.intent||!e.batch||!e.job)throw Error('review_required');
  if(e.state==='restore_uncertain'&&!e.reconciled)throw Error('restore_reconciliation_required');
  const projection=this.projection(s.cache,e.job,e.batch.asset_id);
  if(reviewed?.entry_id!==e.intent.entry_id||projection?.service_entry_context?.link_id!==e.intent.link_id)throw Error('review_required');
  const next=this.stageServiceEntryIn(s,e.job,e.batch.asset_id,reviewed,dependencies,e.intent.occurred_at);
  e.state='replaced';await this.persist(s);return next;
 });}
 receiptPath(e,id){return e.family==='service_entry'?`/service-entry-operations/${encodeURIComponent(id)}`:super.receiptPath(e,id);}
}
module.exports={ServiceEntryClient,ProtectedStore};
