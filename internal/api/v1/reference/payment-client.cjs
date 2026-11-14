'use strict';
const {randomUUID}=require('node:crypto');
const {CommercialClient,ProtectedStore}=require('./commercial-client.cjs');
const clone=value=>JSON.parse(JSON.stringify(value));
const outcomes=['accepted','forbidden','not_found','invalid_payment','invoice_ineligible'];
const fields=['amount','method','date','other_description','reference','note'];

// Financial intent is never replayed by synchronization or receipt lookup. An
// absent receipt is not evidence that money was not received, even after restore.
class PaymentClient extends CommercialClient {
  ownsPending(e){return e.family==='payment'||super.ownsPending(e);}
  purgedOperation(e){return {...super.purgedOperation(e),...(e.family==='payment'?{financialUncertain:!!e.financialUncertain||['unknown','unreceived','locked_review','restore_uncertain'].includes(e.state)}:{})};}
  requirePaymentReconciled(s){
    if(Object.values(s.pending).some(e=>e.family==='payment'&&(e.financialUncertain||['unknown','unreceived','locked_review'].includes(e.state)||e.state==='restore_uncertain'&&!e.reconciled)))throw Error('reconciliation_required');
  }
  async synchronize(kind='invoice',delta=false){
    if(kind!=='invoice')throw Error('document_kind_mismatch');
    await super.synchronize(kind,delta);
    return this.transaction(async s=>{
      try{
        for(const doc of Object.values(s.documents)){
          const r=await this.contact(s,'/invoices/'+doc.legacy_id+'/payment-methods');
          if(r.status!==200)throw Error('synchronization_incomplete');
          doc.paymentMethods=clone(r.data);
        }
        await this.persist(s);return {state:'synchronized',count:Object.keys(s.documents).length};
      }catch(error){if(!this.store.quarantined)await this.withholdCache(s);throw error;}
    });
  }
  async refreshPendingAuthority(s,e,current,kind){
    if(e.family!=='payment')return super.refreshPendingAuthority(s,e,current,kind);
    const r=await this.contact(s,'/invoices/'+current.legacy_id+'/payment-methods');
    if(r.status!==200){e.withheld=true;return false;}
    return true;
  }
  stagePaymentIn(s,target,values){
    this.requirePaymentReconciled(s);
    const doc=s.documents?.[target];
    if(!doc?.job_id||!doc.customer_id||!Array.isArray(doc.paymentMethods)||!values||Object.keys(values).some(k=>!fields.includes(k)))throw Error('review_required');
    if(typeof values.amount!=='string'||!/^\d+\.\d{2}$/.test(values.amount)||BigInt(values.amount.replace('.',''))<=0n||typeof values.date!=='string'||!/^\d{4}-\d{2}-\d{2}$/.test(values.date))throw Error('review_required');
    const method=doc.paymentMethods.find(m=>m.id===values.method);
    if(!method||values.method==='other'&&(typeof values.other_description!=='string'||!values.other_description.trim())||fields.some(k=>values[k]!==undefined&&typeof values[k]!=='string'))throw Error('review_required');
    const id=randomUUID();
    s.pending[id]={family:'payment',kind:'invoice',target,customer:doc.customer_id,state:'pending',reviewed:{document_id:doc.legacy_id,method:clone(method)},intent:{...clone(values),operation_id:id,job_id:doc.job_id,occurred_at:new Date(this.now()).toISOString()}};
    return id;
  }
  async stagePayment(target,values){return this.transaction(async s=>{await this.enforceOfflineAccessWindow(s);const id=this.stagePaymentIn(s,target,values);await this.persist(s);return id;});}
  receiptPath(e,id){return e.family==='payment'?'/payment-operations/'+encodeURIComponent(id):super.receiptPath(e,id);}
  receipt(e,id,r){
    if(e.family!=='payment')return super.receipt(e,id,r);
    if(r?.operation_id!==id||!outcomes.includes(r.outcome)||typeof r.accepted_at!=='string'||!Number.isFinite(Date.parse(r.accepted_at))||r.outcome==='accepted'&&(typeof r.payment_id!=='string'||!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(r.payment_id)||r.payment_id==='00000000-0000-0000-0000-000000000000'))throw Error('receipt_mismatch');
    e.receipt={operation_id:id,outcome:r.outcome,accepted_at:r.accepted_at,...(r.outcome==='accepted'?{payment_id:r.payment_id}:{})};
    delete e.financialUncertain;
    if(e.state!=='replaced')e.state=r.outcome==='accepted'?'accepted':'terminal';
    if(e.state==='accepted'){delete e.intent;delete e.reviewed;}
  }
  receiptMissing(e){
    if(e.family!=='payment')return super.receiptMissing(e);
    if(e.state==='unknown')e.state='unreceived';
    if(e.state==='restore_uncertain')e.lookupMissing=true;
  }
  async receiptAuthority(s,e,code){
    if(e.family!=='payment')return super.receiptAuthority(s,e,code);
    if(['forbidden','not_found'].includes(e.receipt?.outcome)){await this.purge(s);return true;}
    return false;
  }
  async submit(id){return this.transaction(async s=>{
    await this.enforceOfflineAccessWindow(s);const e=s.pending[id];
    if(e?.family!=='payment'||e.state!=='pending'||!e.intent||e.withheld)throw Error('reconciliation_or_review_required');
    this.requirePaymentReconciled(s);return this.postPaymentIn(s,e,id);
  });}
  async postPaymentIn(s,e,id){
    if(e.retryNotBefore&&this.now()<e.retryNotBefore)throw Error('retry_after_required');
    await this.policy(s);e.state='unknown';await this.withholdCache(s);
    const r=await this.contact(s,'/invoices/'+e.reviewed.document_id+'/payments','POST',clone(e.intent));
    if(r.status===200){this.receipt(e,id,r.data);await this.persist(s);}
    else{
      e.transientResponse=(r.status===429||r.status>=500)&&r.error?.recovery_disposition==='retry';
      if(e.transientResponse)e.retryNotBefore=this.now()+this.retryDelay(r.error);
      await this.persistAuthorityLoss(s);
    }
    return clone({state:e.state,receipt:e.receipt,status:r.status,code:r.error?.code});
  }
  async retryTransient(id,{confirm=false}={}){
    if(!confirm)throw Error('review_required');
    const result=await this.reconcile(id);if(result.receipt)return result;
    await this.synchronize();
    return this.transaction(async s=>{
      await this.enforceOfflineAccessWindow(s);const e=s.pending[id];
      if(e?.family!=='payment'||e.state!=='unreceived'||!e.transientResponse||!e.intent||e.withheld)throw Error('review_required');
      return this.postPaymentIn(s,e,id);
    });
  }
  async repairPayment(id,values){return this.transaction(async s=>{
    await this.enforceOfflineAccessWindow(s);const e=s.pending[id],doc=s.documents?.[e?.target];
    if(e?.family!=='payment'||!['terminal','restore_uncertain'].includes(e.state)||!e.intent||e.withheld||!doc||doc.legacy_id!==e.reviewed.document_id||doc.job_id!==e.intent.job_id||doc.customer_id!==e.customer)throw Error('review_required');
    if(e.state==='restore_uncertain'&&!e.reconciled)throw Error('restore_reconciliation_required');
    const next=this.stagePaymentIn(s,e.target,values);e.state='replaced';await this.persist(s);return next;
  });}
  async discard(id){return this.transaction(async s=>{
    const e=s.pending[id];
    if(e?.family==='payment'&&(e.financialUncertain||['unknown','unreceived','locked_review','restore_uncertain'].includes(e.state)))throw Error('reconciliation_required');
    delete s.pending[id];await this.persist(s);
  });}
}
module.exports={PaymentClient,ProtectedStore};
