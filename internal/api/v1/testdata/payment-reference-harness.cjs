'use strict';
const { randomUUID } = require('node:crypto');

// Executable in-memory reference only. It is not a production encrypted cache.
class PaymentReference {
  constructor(request) { this.request=request; this.pending=null; this.restored=false; }
  stage(invoice,job,values) {
    if (this.restored || this.pending && !['accepted','rejected','discarded'].includes(this.pending.state)) throw new Error('Reconcile the original financial intent first');
    if (!Number.isSafeInteger(invoice)||invoice<1||!Number.isSafeInteger(job)||job<1) throw new Error('Original Invoice and Job required');
    const intent=Object.freeze({...values,job_id:job,operation_id:randomUUID()});
    this.pending=Object.defineProperties({state:'pending',receipt:null},{invoice:{value:invoice,enumerable:true},intent:{value:intent,enumerable:true}});
    return this.pending;
  }
  async submit(entry) {
    if (entry!==this.pending||entry.state!=='pending'||this.restored) throw new Error('Explicit original-intent review required');
    entry.state='unknown';
    try {
      const response=await this.request('POST',`/invoices/${entry.invoice}/payments`,entry.intent);
      if(response.status===200) this.acceptReceipt(entry,response.body.data);
      else await this.inspect(entry);
    } catch { /* Preserve the original unknown intent. */ }
    return entry;
  }
  acceptReceipt(entry,receipt) {
    if(receipt?.operation_id!==entry.intent.operation_id) {entry.state='locked';return;}
    entry.receipt=Object.freeze(receipt);entry.state=receipt.outcome==='accepted'?'accepted':'rejected';
  }
  async inspect(entry) {
    const response=await this.request('GET',`/payment-operations/${entry.intent.operation_id}`);
    if(response.status===200) this.acceptReceipt(entry,response.body.data);
    else entry.state=response.status===404?'unknown':'locked';
    return entry;
  }
  restoreDetected() {this.restored=true;if(this.pending)this.pending.state='locked';}
  async reconcileAfterFreshAuthentication(entry) {
    if(!this.restored) throw new Error('No restore review pending');
    await this.inspect(entry);
    // A missing receipt cannot reconstruct post-backup effects. No blind replay.
    if(entry.receipt&&['accepted','rejected'].includes(entry.state)) this.restored=false;
    return entry;
  }
  confirmUnreceivedAfterReview(entry) {
    if(entry!==this.pending||entry.state!=='unknown'||entry.receipt) throw new Error('Inspect original receipt and explicitly reconcile history first');
    entry.state='discarded';this.restored=false;
  }
}
module.exports={PaymentReference};
