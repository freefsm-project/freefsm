'use strict';
const {randomUUID} = require('node:crypto');
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
    if (receipt?.operation_id!==record.intent.operation_id || (accepted ? !Array.isArray(receipt.delivery_ids) || !receipt.delivery_ids.length || receipt.delivery_ids.some(id=>!Number.isSafeInteger(id)||id<=0) : !['source_drift','forbidden','not_found','invalid_attachments'].includes(receipt?.outcome))){this.#lock();return;}
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
module.exports={DeliveryReference};
