'use strict';
const {randomUUID} = require('node:crypto');
const {CommercialClient,ProtectedStore}=require('./commercial-client.cjs');
const clone=value=>JSON.parse(JSON.stringify(value));
const creationOutcomes=['source_drift','forbidden','not_found','invalid_attachments','invalid_dependency','dependency_failed'];
const retryOutcomes=['source_drift','forbidden','not_found','cancelled','invalid_state'];
const validReceipt=(r,id,outcomes)=>r?.operation_id===id&&(r.outcome==='accepted'?Array.isArray(r.delivery_ids)&&r.delivery_ids.length>0&&r.delivery_ids.every(n=>Number.isSafeInteger(n)&&n>0):outcomes.includes(r.outcome));
const messageFields=['pdf_version_id','confirm_stale_pdf','to','cc','bcc','subject','text_body','html_body','extra_file_ids'];
const messageOf=intent=>Object.fromEntries(messageFields.filter(k=>Object.hasOwn(intent,k)).map(k=>[k,clone(intent[k])]));

// Uses the same encrypted store, finite lease and original-parent projection as
// commercial work. Receipt reconciliation never submits a Delivery command.
class DeliveryClient extends CommercialClient {
  ownsPending(e){return ['delivery','delivery_retry'].includes(e.family)||super.ownsPending(e);}
  async refreshPendingAuthority(s,e,current,kind){
    if(e.family!=='delivery')return super.refreshPendingAuthority(s,e,current,kind);
    const r=await this.contact(s,'/'+kind+'s/'+current.legacy_id+'/deliveries/review','POST',messageOf(e.intent));
    if(r.status===404){Object.assign(e,this.purgedOperation(e));delete e.intent;delete e.reviewed;return false;}
    if(r.status!==200){e.withheld=true;return false;}
    if(r.data?.document_id!==e.reviewed.document_id||r.data.job_id!==e.intent.job_id){Object.assign(e,this.purgedOperation(e));delete e.intent;delete e.reviewed;return false;}
    return true;
  }
  requireDeliveryReconciled(s){
    if(Object.values(s.pending).some(e=>['delivery','delivery_retry'].includes(e.family)&&(['unknown','unreceived'].includes(e.state)||e.state==='restore_uncertain'&&!e.reconciled)))throw Error('reconciliation_required');
  }
  async reviewDelivery(target,message){return this.transaction(async s=>{
    await this.enforceOfflineAccessWindow(s);const doc=s.documents?.[target];
    if(!doc||!doc.job_id||!message||Object.keys(message).some(k=>!messageFields.includes(k)))throw Error('review_required');
    await this.policy(s);
    const r=await this.contact(s,'/'+s.commercialKind+'s/'+doc.legacy_id+'/deliveries/review','POST',clone(message));
    if(r.status!==200){await this.withholdCache(s);throw Error(r.error?.code||'review_unavailable');}
    if(r.data?.document_id!==doc.legacy_id||r.data.job_id!==doc.job_id||r.data.pdf_version_id!==message.pdf_version_id||!r.data.review_token)throw Error('review_mismatch');
    doc.deliveryReview={message:clone(message),review:clone(r.data)};await this.persist(s);return clone(r.data);
  });}
  stageDeliveryIn(s,target,reviewToken,pdfOperation){
    this.requireDeliveryReconciled(s);const doc=s.documents?.[target],review=doc?.deliveryReview;
    if(!review||review.review.review_token!==reviewToken)throw Error('review_required');
    if(pdfOperation!==undefined)this.reviewedDependencies([pdfOperation]);
    const id=randomUUID();
    s.pending[id]={family:'delivery',kind:s.commercialKind,target,customer:doc.customer_id,state:'pending',reviewed:clone(review.review),intent:{...clone(review.message),operation_id:id,job_id:doc.job_id,review_token:reviewToken,occurred_at:new Date(this.now()).toISOString(),...(pdfOperation===undefined?{}:{pdf_operation_id:pdfOperation})}};
    return id;
  }
  async stageDelivery(target,reviewToken,pdfOperation){return this.transaction(async s=>{
    await this.enforceOfflineAccessWindow(s);const id=this.stageDeliveryIn(s,target,reviewToken,pdfOperation);await this.persist(s);return id;
  });}
  stageRetryIn(s,target,delivery,reason){
    this.requireDeliveryReconciled(s);const doc=s.documents?.[target];
    if(!doc?.job_id||!Number.isSafeInteger(delivery)||delivery<=0||typeof reason!=='string'||!reason.trim())throw Error('review_required');
    const id=randomUUID();
    s.pending[id]={family:'delivery_retry',kind:s.commercialKind,target,customer:doc.customer_id,state:'pending',reviewed:{document_id:doc.legacy_id,job_id:doc.job_id},intent:{operation_id:id,document_type:s.commercialKind,document_id:doc.legacy_id,job_id:doc.job_id,delivery_id:delivery,reason,occurred_at:new Date(this.now()).toISOString()}};
    return id;
  }
  async stageRetry(target,delivery,reason){return this.transaction(async s=>{
    await this.enforceOfflineAccessWindow(s);const id=this.stageRetryIn(s,target,delivery,reason);await this.persist(s);return id;
  });}
  receiptPath(e,id){return ['delivery','delivery_retry'].includes(e.family)?'/delivery-operations/'+encodeURIComponent(id):super.receiptPath(e,id);}
  receipt(e,id,r){
    if(!['delivery','delivery_retry'].includes(e.family))return super.receipt(e,id,r);
    if(!validReceipt(r,id,e.family==='delivery'?creationOutcomes:retryOutcomes))throw Error('receipt_mismatch');
    // Allowlisted receipt metadata is safe even after parent/session scope loss.
    e.receipt={operation_id:id,outcome:r.outcome,...(r.outcome==='accepted'?{delivery_ids:[...r.delivery_ids]}:{})};
    if(e.state!=='replaced')e.state=r.outcome==='accepted'?'accepted':'terminal';
    if(e.state==='accepted'){delete e.intent;delete e.reviewed;}
  }
  receiptMissing(e){
    if(!['delivery','delivery_retry'].includes(e.family))return super.receiptMissing(e);
    if(e.state==='unknown')e.state=e.terminalResponse?'terminal_missing':'unreceived';
    if(e.state==='restore_uncertain')e.lookupMissing=true;
  }
  async submit(id){return this.transaction(async s=>{
    await this.enforceOfflineAccessWindow(s);const e=s.pending[id];
    if(!['delivery','delivery_retry'].includes(e?.family)||e.state!=='pending'||!e.intent||e.withheld)throw Error('reconciliation_or_review_required');
    return this.postDeliveryIn(s,e,id);
  });}
  async postDeliveryIn(s,e,id){
    if(e.retryNotBefore&&this.now()<e.retryNotBefore)throw Error('retry_after_required');
    await this.policy(s);e.state='unknown';await this.persistAuthorityLoss(s);
    const path=e.family==='delivery_retry'?'/delivery-retries':'/'+e.kind+'s/'+e.reviewed.document_id+'/deliveries';
    const r=await this.contact(s,path,'POST',clone(e.intent));
    if(r.status===200){this.receipt(e,id,r.data);await this.persist(s);}
    else{if(r.status===429||r.status>=500)e.retryNotBefore=this.now()+this.retryDelay(r.error);else e.terminalResponse=true;await this.withholdCache(s);}
    return clone({state:e.state,receipt:e.receipt,status:r.status,code:r.error?.code});
  }
  async retryUnreceived(id,{confirm=false}={}){
    if(!confirm)throw Error('review_required');
    const kind=await this.transaction(async s=>{const e=s.pending[id];if(!['delivery','delivery_retry'].includes(e?.family)||e.state!=='unreceived'||!e.intent||e.receipt)throw Error('review_required');return e.kind;});
    const result=await this.reconcile(id);if(result.receipt||result.state!=='unreceived')return result;
    await this.synchronize(kind);
    return this.transaction(async s=>{
      await this.enforceOfflineAccessWindow(s);const e=s.pending[id];
      if(e.state!=='unreceived'||!e.intent||e.receipt||e.withheld)throw Error('review_required');
      if(e.family==='delivery'){
        const r=await this.contact(s,'/'+e.kind+'s/'+e.reviewed.document_id+'/deliveries/review','POST',messageOf(e.intent));
        if(r.status!==200||r.data?.document_id!==e.reviewed.document_id||r.data.job_id!==e.intent.job_id||r.data.review_token!==e.intent.review_token){e.state='terminal_missing';await this.withholdCache(s);throw Error('review_required');}
      }
      return this.postDeliveryIn(s,e,id);
    });
  }
  async repairDelivery(id,reviewToken,pdfOperation){return this.transaction(async s=>{
    await this.enforceOfflineAccessWindow(s);const e=this.repairableDelivery(s,id,'delivery');
    const next=this.stageDeliveryIn(s,e.target,reviewToken,pdfOperation);e.state='replaced';await this.persist(s);return next;
  });}
  async repairRetry(id,reason){return this.transaction(async s=>{
    await this.enforceOfflineAccessWindow(s);const e=this.repairableDelivery(s,id,'delivery_retry');
    const next=this.stageRetryIn(s,e.target,e.intent.delivery_id,reason);e.state='replaced';await this.persist(s);return next;
  });}
  repairableDelivery(s,id,family){
    const e=s.pending[id],doc=s.documents?.[e?.target];
    if(e?.family!==family||!['terminal','terminal_missing','restore_uncertain'].includes(e.state)||!e.intent||e.withheld||!doc||doc.job_id!==e.intent.job_id||doc.customer_id!==e.customer||doc.legacy_id!==e.reviewed.document_id)throw Error('review_required');
    if(e.state==='restore_uncertain'&&!e.reconciled)throw Error('restore_reconciliation_required');
    return e;
  }
}
const freeze = value => {
  if (value && typeof value === 'object') {for (const child of Object.values(value)) freeze(child); Object.freeze(value);}
  return value;
};

// Executable protocol reference, not a durable encrypted mobile cache. Staging
// freezes reviewed intent; it never generates/selects a PDF or retargets a File.
class DeliveryReference {
  #request; #context; #records = new Map(); #locked = false; #generation = 0;
  constructor(request, context) {
    if (!context?.client || !context?.epoch) throw new Error('authenticated_context_required');
    this.#context = Object.freeze({...context});
    this.#request = (method,path,input) => request(method,path,input,this.#context);
  }
  stage(kind, review, message, {occurredAt} = {}) {
    if (this.#locked || !['invoice','estimate'].includes(kind) || !Number.isSafeInteger(review.document_id) || review.document_id <= 0 || !Number.isSafeInteger(review.job_id) || review.job_id <= 0 || !review.review_token || review.pdf_version_id !== message.pdf_version_id) throw new Error('review_required');
    const intent = freeze(JSON.parse(JSON.stringify({...message,operation_id:randomUUID(),job_id:review.job_id,review_token:review.review_token,...(occurredAt ? {occurred_at:occurredAt} : {})})));
    const record = {kind,document:review.document_id,intent,state:'pending',receipt:null,busy:false,terminal:false};
    const entry = Object.freeze({kind,document:record.document,intent,get state(){return record.state;},get receipt(){return record.receipt;}});
    this.#records.set(entry,record); return entry;
  }
  #record(entry) {const record=this.#records.get(entry);if (!record || record.busy) throw new Error('operation_pending');return record;}
  #lock() {this.#locked=true;++this.#generation;for(const record of this.#records.values())record.state='locked';}
  restoreDetected(){this.#lock();}
  authenticationChanged(){this.#lock();}
  #same(response) {
    if ([401,403].includes(response.status) || response.body?.error?.code==='instance_restored' || response.client!==this.#context.client || response.epoch!==this.#context.epoch){this.#lock();return false;}return true;
  }
  #receipt(record,receipt) {
    const accepted=receipt?.outcome==='accepted';
    if (!validReceipt(receipt,record.intent.operation_id,creationOutcomes)){this.#lock();return;}
    record.receipt=freeze(structuredClone(receipt));record.state=accepted?'accepted':'needs_review';
  }
  #message(record) {
    const {operation_id,review_token,job_id,occurred_at,...message}=record.intent;return message;
  }
  async #inspect(record,generation) {
    const response=await this.#request('GET',`/delivery-operations/${record.intent.operation_id}`);
    if(generation!==this.#generation)return;
    if(response.status>=500 || response.status===429){record.state='unknown';return;}
    if(!this.#same(response))return;
    if(response.status===200){this.#receipt(record,response.body?.data);return;}
    if(response.status!==404 || response.body?.error?.code!=='not_found' || record.receipt){this.#lock();return;}
    if(record.terminal){record.state='needs_review';return;}
    // This POST is read-only review. Ordinary absence never authorizes a new
    // effect by itself, and review can never substitute different identities.
    const current=await this.#request('POST',`/${record.kind}s/${record.document}/deliveries/review`,this.#message(record));
    if(generation!==this.#generation)return;
    if(current.status>=500 || current.status===429){record.state='unknown';return;}
    if(!this.#same(current))return;
    const review=current.body?.data;
    record.state=current.status===200 && review.document_id===record.document && review.job_id===record.intent.job_id && review.review_token===record.intent.review_token?'retryable':'needs_review';
  }
  async reconcile(entry) {
    const record=this.#record(entry);if(this.#locked || !['unknown','retryable'].includes(record.state))throw new Error('review_required');
    record.busy=true;const generation=this.#generation;
    try{await this.#inspect(record,generation);}catch{if(generation===this.#generation)record.state='unknown';}finally{record.busy=false;}
  }
  async submit(entry) {
    const record=this.#record(entry);if(this.#locked)throw new Error('review_required');
    if(record.state==='unknown'){await this.reconcile(entry);return;}
    if(record.state!=='pending')throw new Error('review_required');return this.#post(record);
  }
  async retryUnreceived(entry,{confirm=false}={}) {
    const record=this.#record(entry);if(this.#locked || !confirm || record.state!=='retryable' || record.receipt)throw new Error('review_required');
    await this.reconcile(entry);if(record.state==='retryable'&&!this.#locked)return this.#post(record);
  }
  async #post(record) {
    record.busy=true;record.state='unknown';const generation=this.#generation;
    try {
      const response=await this.#request('POST',`/${record.kind}s/${record.document}/deliveries`,record.intent);
      if(generation!==this.#generation || response.status>=500 || response.status===429)return;
      if(!this.#same(response))return;
      if(response.status===200){this.#receipt(record,response.body?.data);return;}
      record.terminal=true;await this.#inspect(record,generation);
    }catch{if(generation===this.#generation)record.state='unknown';}finally{record.busy=false;}
  }
}
module.exports={DeliveryClient,ProtectedStore,DeliveryReference};
