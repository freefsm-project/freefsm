'use strict';
const {randomUUID}=require('node:crypto');
const {ProtectedFieldClient,ProtectedStore}=require('./protected-field-client.cjs');
const clone=value=>JSON.parse(JSON.stringify(value,(_key,v)=>{if(typeof v==='number'&&!Number.isFinite(v))throw Error('invalid_document');return v;}));
const editFields=new Set(['title','notes','terms','tax_rate','invoice_date','due_date','valid_through','line_items','custom_fields','adjustments']);

// One document kind per protected store. Network, review, staging and submission
// are separate explicit actions; synchronization never rewrites staged intent.
class CommercialClient extends ProtectedFieldClient {
 async withholdCache(s){
  s.synced=false;s.documents={};s.commercialCursor=null;s.pdfSources={};s.pdfSelections={};
  // A shared-store observation from another field adapter is not fresh authority.
  s.cache={};s.notes={};s.items={};s.clocks={};s.clockJobs={};
  s.clockCapabilities=null;s.clockLease=null;s.clockIssuance=null;s.clockCursor=null;s.syncCursor=null;
  await this.persistAuthorityLoss(s);
 }
 async purge(s,issuance=null){s.documents={};s.commercialCursor=null;s.items={};s.pdfSources={};s.pdfSelections={};s.pdfChoices={};delete s.pdfDownloadWarning;return super.purge(s,issuance);}
 beginRestore(s){delete s.pdfDownloadWarning;super.beginRestore(s);}
 async contact(s,...args){
  try{return await super.contact(s,...args);}
  catch(error){if(!this.store.quarantined)await this.withholdCache(s);throw error;}
 }
 async policy(s,...args){
  try{return await super.policy(s,...args);}
  catch(error){if(!this.store.quarantined)await this.withholdCache(s);throw error;}
 }
 async receiptContactComplete(s){if(!this.store.quarantined)await this.withholdCache(s);}
 async synchronize(kind,delta=false){return this.transaction(async s=>{
  if(!['estimate','invoice'].includes(kind)||s.commercialKind&&s.commercialKind!==kind)throw Error('document_kind_mismatch');
  if(delta){await this.enforceOfflineAccessWindow(s);if(!s.commercialCursor)throw Error('synchronization_incomplete');}
  const previous=delta?clone(s.documents):{},cursor=delta?s.commercialCursor:null;
  await this.withholdCache(s);await this.policy(s);
  const page=await this.pages(s,'/sync/'+kind+'s?scope=assigned',cursor,previous);
  for(const [id,e] of Object.entries(s.pending)){
   // This adapter has not refreshed another domain's authority. Never unlock
   // inherited private intent merely because this commercial snapshot succeeded.
    if(!['commercial','pdf'].includes(e.family)){s.pending[id]=this.purgedOperation(e);continue;}
   const current=page.records[e.target];
    if(e.intent&&(!current||e.kind!==kind||current.job_id!==e.intent.job_id||current.customer_id!==e.customer||e.reviewed?.totals&&!current.totals)){
     if(e.family==='pdf')s.pending[id]=this.purgedOperation(e);
     else{delete e.intent;delete e.reviewed;e.state='purged';}
    }else if(e.intent){
     if(e.family==='pdf'){
      // Unknown outcomes are receipt-first. A document sync must not turn an
      // uninspected request into a fresh source review or expose its old preview.
      if(e.state==='unknown'){e.withheld=true;continue;}
      // PDF preview authority is independent of commercial pricing. Refresh its
      // original parent's Generate authority without replacing the reviewed token.
      const r=await this.contact(s,'/'+kind+'s/'+current.legacy_id+'/pdf/source');
      if(r.status===404){s.pending[id]=this.purgedOperation(e);await this.persistAuthorityLoss(s);continue;}
      if(r.status!==200)throw Error('synchronization_incomplete');
      if(r.data?.document_id!==e.reviewed.document_id||r.data.job_id!==e.intent.job_id){s.pending[id]=this.purgedOperation(e);continue;}
      delete e.withheld;
     }
     if(s.restore&&!['replaced','purged'].includes(e.state))e.state='restore_uncertain';
   }
  }
   for(const [target,choice] of Object.entries(s.pdfChoices||{})){
    const doc=page.records[target];
    if(!doc||choice.kind!==kind||choice.document!==doc.legacy_id||choice.job!==doc.job_id||choice.customer!==doc.customer_id)delete s.pdfChoices[target];
   }
   s.documents=page.records;s.commercialCursor=page.cursor;s.commercialKind=kind;s.synced=true;s.restore=false;
  await this.persistAuthorityLoss(s);return {state:'synchronized',count:Object.keys(s.documents).length};
 });}
 async view(){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const pending=clone(s.pending);
  for(const e of Object.values(pending))if(e.withheld){delete e.intent;delete e.reviewed;}
  return clone({documents:s.documents||{},pending});
 });}
  stageIn(s,target,revision,values,action='edit'){
   this.requireReconciled(s);
  const doc=s.documents?.[target];
  if(!doc||doc.revision!==revision||!doc.job_id||!doc.customer_id||!doc.actions?.[action])throw Error('review_required');
  if(!values||typeof values!=='object'||Array.isArray(values))throw Error('review_required');
  let intent;
  if(action==='edit'){
   if(!Object.keys(values).length||Object.keys(values).some(k=>!editFields.has(k)))throw Error('review_required');
   intent={...clone(values),customer_id:doc.customer_id};
  }else if(action==='convert'||action==='revert'){
   if(action==='convert'){
    if(s.commercialKind!=='estimate'||Object.keys(values).some(k=>k!=='custom_fields'))throw Error('review_required');
    intent=clone(values);
   }else{
    if(s.commercialKind!=='invoice'||Object.keys(values).some(k=>k!=='cycle_id')||!doc.cycle_id||values.cycle_id!==doc.cycle_id)throw Error('exact_cycle_review_required');
    intent={cycle_id:doc.cycle_id};
   }
   intent.occurred_at=new Date(this.now()).toISOString();
  }else{
   if(!['status','finalize'].includes(action)||Object.keys(values).some(k=>!['id','meaning_revision','category'].includes(k)))throw Error('review_required');
   const choice=doc.workflow?.find(w=>w.id===values.id&&w.meaning_revision===values.meaning_revision&&w.category===values.category);
   if(!choice)throw Error('workflow_reselection_required');
   intent={status_id:choice.id,meaning_revision:choice.meaning_revision};
  }
   const id=randomUUID();
   const reviewed=clone(doc);delete reviewed.conversion_history;
   s.pending[id]={family:'commercial',kind:s.commercialKind,target,customer:doc.customer_id,action,state:'pending',reviewed,intent:{...intent,job_id:doc.job_id,revision:doc.revision,operation_id:id}};
  return id;
 }
 async stageEdit(target,revision,values){return this.stage(target,revision,values,'edit');}
 async stageConversion(target,revision,values){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);
  const id=this.stageIn(s,target,revision,values,s.commercialKind==='estimate'?'convert':'revert');
  await this.persist(s);return id;
 });}
  async stageLifecycle(target,revision,choice,action){return this.stage(target,revision,choice,action);}
  requireReconciled(s){
   if(Object.values(s.pending).some(e=>e.family==='commercial'&&(e.state==='unknown'||e.state==='restore_uncertain'&&!e.reconciled)))throw Error('reconciliation_required');
  }
  validateBatch(s){this.requireReconciled(s);}
  // Persist the entire unknown group and withhold observations in one protected
  // transition. A failed checkpoint quarantines the store before any transport.
  async batchCheckpoint(s){await this.withholdCache(s);}
  async batchFailure(s){await this.withholdCache(s);}
  async stageBatch(target,revision,values,action,dependencies){return this.transaction(async s=>{
   await this.enforceOfflineAccessWindow(s);
   if(!['edit','status','finalize','convert','revert'].includes(action))throw Error('unsupported_batch_action');
   const depends_on=this.reviewedDependencies(dependencies);
   const id=this.stageIn(s,target,revision,values,action),e=s.pending[id];
   e.batch={type:e.kind+'.'+e.action,target:String(e.reviewed.legacy_id),depends_on};
   await this.persist(s);return id;
  });}
  async conversionHistory(target,cursor=''){return this.transaction(async s=>{
   await this.enforceOfflineAccessWindow(s);
   const doc=s.documents?.[target];
   if(!s.synced||!doc||typeof cursor!=='string')throw Error('review_required');
   await this.policy(s);
   const r=await this.contact(s,'/'+s.commercialKind+'s/'+doc.legacy_id+'/conversion-history'+(cursor?'?cursor='+encodeURIComponent(cursor):''),'GET');
   if(r.status!==200){await this.withholdCache(s);throw Error(r.error?.code||'history_unavailable');}
   // One bounded page, encrypted with its current parent. Synchronization,
   // expiry and authority loss use the same withholding/purge owner as documents.
   doc.conversion_history=clone(r.data);await this.persist(s);
   return clone(doc.conversion_history);
  });}
  batchOperation(e,id){
   if(e.family!=='commercial'||e.withheld)throw Error('reconciliation_or_review_required');
   const commercial=clone(e.intent);delete commercial.operation_id;
   return {operation_id:id,...clone(e.batch),commercial};
  }
 async stage(target,revision,values,action){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const id=this.stageIn(s,target,revision,values,action);await this.persist(s);return id;
 });}
 receiptPath(e,id){if(e.family!=='commercial')throw Error('unknown_operation');return '/commercial-operations/'+encodeURIComponent(id);}
 receipt(e,id,r){
  if(r?.operation_id!==id||typeof r.outcome!=='string')throw Error('receipt_mismatch');
  e.receipt=clone(r);if(e.state!=='replaced')e.state=r.outcome==='accepted'?'accepted':'terminal';
  if(e.state==='accepted'){delete e.intent;delete e.reviewed;}
 }
  async receiptAuthority(s,e,code){
   const outcome=e.receipt?.outcome||code;
   if(outcome==='forbidden'){await this.purge(s);return true;}
    if(outcome==='accepted'&&['convert','revert'].includes(e.action)){
     if(s.pdfChoices)delete s.pdfChoices[e.target];
    // The receipt establishes parent loss now, not at the next synchronization.
    // Other original-parent reviews cannot follow the new active document.
    for(const [id,pending] of Object.entries(s.pending)){
     if(pending!==e&&['commercial','pdf'].includes(pending.family)&&pending.kind===e.kind&&pending.target===e.target)s.pending[id]=this.purgedOperation(pending);
    }
    await this.withholdCache(s);return true;
   }
   if(outcome!=='accepted'){await this.withholdCache(s);return true;}
  return false;
 }
  async submit(id){return this.transaction(async s=>{
   await this.enforceOfflineAccessWindow(s);const e=s.pending[id];
   if(e?.batch)throw Error('transport_identity_mismatch');
   if(e?.family!=='commercial'||e.state!=='pending'||!e.intent||e.withheld)throw Error('reconciliation_or_review_required');
   this.requireReconciled(s);
  if(e.retryNotBefore&&this.now()<e.retryNotBefore)throw Error('retry_after_required');
  await this.policy(s);e.state='unknown';await this.persist(s);
  const path='/'+e.kind+'s/'+e.reviewed.legacy_id+(e.action==='edit'?'':'/'+(e.action==='revert'?'revert-to-estimate':e.action));
  const r=await this.contact(s,path,e.action==='edit'?'PATCH':'POST',e.intent);
  if(r.status===200){this.receipt(e,id,r.data);await this.receiptAuthority(s,e,r.data.outcome);await this.persist(s);}
  else{if(r.status===429||r.status>=500)e.retryNotBefore=this.now()+this.retryDelay(r.error);await this.withholdCache(s);}
  return clone({state:e.state,receipt:e.receipt,status:r.status,code:r.error?.code});
 });}
 async compare(id){
  const kind=await this.transaction(async s=>{
   const e=s.pending[id];if(e?.family!=='commercial')throw Error('unknown_operation');
   if(e.state==='unknown')throw Error('reconciliation_required');return e.kind;
  });
  await this.synchronize(kind);
  return this.transaction(async s=>{
   await this.enforceOfflineAccessWindow(s);const e=s.pending[id],current=s.documents[e.target];
   if(!e.intent||e.withheld||!current)throw Error('comparison_unavailable');
   return clone({reviewed:e.reviewed,current,proposed:e.intent});
  });
 }
   repairIn(s,id,revision,values,dependencies){
    const e=s.pending[id],doc=s.documents?.[e?.target];
    if(e?.family!=='commercial'||!['terminal','restore_uncertain'].includes(e.state)||!e.intent||e.withheld)throw Error('review_required');
    if(!!e?.batch!==(dependencies!==undefined))throw Error('transport_identity_mismatch');
    if(!doc||doc.job_id!==e.intent.job_id||doc.customer_id!==e.customer)throw Error('review_required');
   if(e.state==='restore_uncertain'&&!e.reconciled)throw Error('restore_reconciliation_required');
   if(e.action==='revert'&&(doc.cycle_id!==e.intent.cycle_id||values?.cycle_id!==e.intent.cycle_id))throw Error('exact_cycle_review_required');
   const depends_on=dependencies===undefined?undefined:this.reviewedDependencies(dependencies);
   const next=this.stageIn(s,e.target,revision,values,e.action);
   if(depends_on)s.pending[next].batch={type:e.batch.type,target:e.batch.target,depends_on};
   e.state='replaced';return next;
  }
  async repair(id,revision,values){return this.transaction(async s=>{
   await this.enforceOfflineAccessWindow(s);const next=this.repairIn(s,id,revision,values);await this.persist(s);return next;
  });}
  async repairBatch(id,revision,values,dependencies){return this.transaction(async s=>{
   await this.enforceOfflineAccessWindow(s);
   const next=this.repairIn(s,id,revision,values,this.reviewedDependencies(dependencies));await this.persist(s);return next;
  });}
}
module.exports={CommercialClient,ProtectedStore};
