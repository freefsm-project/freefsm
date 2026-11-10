'use strict';
const { randomUUID } = require('node:crypto');
const {CommercialClient,ProtectedStore}=require('./commercial-client.cjs');
const clone=value=>JSON.parse(JSON.stringify(value));
const terminalOutcomes=['source_drift','dependency_failed','invalid_dependency','forbidden','not_found'];
const validReceipt=(receipt,id)=>receipt?.operation_id===id&&(receipt.outcome==='accepted'?Number.isSafeInteger(receipt.version_id)&&receipt.version_id>0:terminalOutcomes.includes(receipt.outcome));
const matchesSource=(source,document,intent)=>source?.document_id===document&&source.job_id===intent.job_id&&source.source_token===intent.source_token;

// The protected document adapter owns durable intent. No constructor, sync,
// receipt lookup or source comparison sends a generation request.
class PDFClient extends CommercialClient {
 async view(){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const pending=clone(s.pending);
  for(const e of Object.values(pending))if(e.withheld){delete e.intent;delete e.reviewed;}
  return clone({documents:s.documents||{},pending,sources:s.pdfSources||{},selections:s.pdfSelections||{}});
 });}
 async reviewSource(target){return this.transaction(s=>this.reviewSourceIn(s,target));}
 async reviewSourceIn(s,target){
  await this.enforceOfflineAccessWindow(s);
  const doc=s.documents?.[target];
  if(!doc||!doc.job_id)throw Error('review_required');
  await this.policy(s);
  const r=await this.contact(s,'/'+s.commercialKind+'s/'+doc.legacy_id+'/pdf/source');
  if(r.status!==200){await this.withholdCache(s);throw Error(r.error?.code||'source_unavailable');}
  if(r.data?.document_id!==doc.legacy_id||r.data.job_id!==doc.job_id||typeof r.data.source_token!=='string'||!r.data.source_token)throw Error('source_mismatch');
  (s.pdfSources||={})[target]=clone(r.data);await this.persist(s);return clone(r.data);
 }
 stagePDFIn(s,target,sourceToken,dependencies=[]){
  if(Object.values(s.pending).some(e=>e.family==='pdf'&&(['unknown','unreceived'].includes(e.state)||e.state==='restore_uncertain'&&!e.reconciled)))throw Error('reconciliation_required');
  const doc=s.documents?.[target],review=s.pdfSources?.[target];
  if(!doc||!review||review.source_token!==sourceToken||review.document_id!==doc.legacy_id||review.job_id!==doc.job_id)throw Error('review_required');
  const id=randomUUID();
  s.pending[id]={family:'pdf',kind:s.commercialKind,target,customer:doc.customer_id,state:'pending',reviewed:clone(review),intent:{operation_id:id,job_id:review.job_id,source_token:sourceToken,depends_on:this.reviewedDependencies(dependencies),occurred_at:new Date(this.now()).toISOString()}};
  return id;
 }
 async stagePDF(target,sourceToken,dependencies=[]){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const id=this.stagePDFIn(s,target,sourceToken,dependencies);await this.persist(s);return id;
 });}
 receiptPath(e,id){return e.family==='pdf'?'/pdf-operations/'+encodeURIComponent(id):super.receiptPath(e,id);}
 receipt(e,id,r){
  if(e.family!=='pdf')return super.receipt(e,id,r);
  if(!validReceipt(r,id))throw Error('receipt_mismatch');
  // Only safe receipt metadata survives resource access loss.
  e.receipt=clone(r);if(e.state!=='replaced')e.state=r.outcome==='accepted'?'accepted':'terminal';
  if(e.state==='accepted'){delete e.intent;delete e.reviewed;}
 }
 receiptMissing(e){
  if(e.family!=='pdf')return super.receiptMissing(e);
  if(e.state==='unknown')e.state=e.terminalResponse?'terminal_missing':'unreceived';
  if(e.state==='restore_uncertain')e.lookupMissing=true;
 }
 async submit(id){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const e=s.pending[id];
  if(e?.family!=='pdf'||e.state!=='pending'||!e.intent||e.withheld)throw Error('reconciliation_or_review_required');
  return this.postIn(s,e,id);
 });}
 async postIn(s,e,id){
  if(e.retryNotBefore&&this.now()<e.retryNotBefore)throw Error('retry_after_required');
  await this.policy(s);
  e.state='unknown';await this.persistAuthorityLoss(s);
  const r=await this.contact(s,'/'+e.kind+'s/'+e.reviewed.document_id+'/pdf/generate','POST',e.intent);
  if(r.status===200){this.receipt(e,id,r.data);await this.persist(s);}
  else{if(r.status===429||r.status>=500)e.retryNotBefore=this.now()+this.retryDelay(r.error);else e.terminalResponse=true;await this.withholdCache(s);}
  return clone({state:e.state,receipt:e.receipt,status:r.status,code:r.error?.code});
 }
 async compare(id){
  const kind=await this.transaction(async s=>{
   const e=s.pending[id];if(e?.family!=='pdf'||!e.intent)throw Error('comparison_unavailable');
   if(!['unreceived','terminal','terminal_missing','restore_uncertain'].includes(e.state))throw Error('reconciliation_required');
   return e.kind;
  });
  await this.synchronize(kind);
  return this.transaction(async s=>{
   const e=s.pending[id];if(!e.intent||e.withheld)throw Error('comparison_unavailable');
   const current=await this.reviewSourceIn(s,e.target);
   return clone({reviewed:e.reviewed,current,proposed:e.intent,same_source:matchesSource(current,e.reviewed.document_id,e.intent)});
  });
 }
 async retryUnreceived(id,{confirm=false}={}){
  if(!confirm)throw Error('review_required');
  // A new lookup is mandatory even after the user compared the original source.
  // Receipt contact withholds authority; synchronize before source comparison.
  const kind=await this.transaction(async s=>{
   const e=s.pending[id];if(e?.family!=='pdf'||e.state!=='unreceived'||!e.intent||e.receipt)throw Error('review_required');return e.kind;
  });
  const result=await this.reconcile(id);
  if(result.receipt||result.state!=='unreceived')return result;
  await this.synchronize(kind);
  return this.transaction(async s=>{
   const e=s.pending[id];if(e.state!=='unreceived'||!e.intent||e.receipt)throw Error('review_required');
   const current=await this.reviewSourceIn(s,e.target);
   if(!matchesSource(current,e.reviewed.document_id,e.intent)){e.state='terminal_missing';await this.persist(s);throw Error('source_drift');}
   return this.postIn(s,e,id);
  });
 }
 async repair(id,sourceToken,dependencies=[]){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const e=s.pending[id];
  if(e?.family!=='pdf'||!e.intent||e.withheld||!['terminal','terminal_missing','restore_uncertain'].includes(e.state))throw Error('review_required');
  if(e.state==='restore_uncertain'&&!e.reconciled)throw Error('restore_reconciliation_required');
  const doc=s.documents?.[e.target];
  if(!doc||doc.job_id!==e.intent.job_id||doc.customer_id!==e.customer)throw Error('review_required');
  const next=this.stagePDFIn(s,e.target,sourceToken,dependencies);e.state='replaced';await this.persist(s);return next;
 });}
 async versions(target){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const doc=s.documents?.[target];if(!doc)throw Error('review_required');
  await this.policy(s);
  const r=await this.contact(s,'/'+s.commercialKind+'s/'+doc.legacy_id+'/pdf/versions');
  if(r.status!==200)return this.failedPDFRead(s,target,r,'versions_unavailable');
  const old=s.pdfSelections?.[target],choice=s.pdfChoices?.[target];
  // Refreshing defaults must never replace a user's explicit exact selection.
  (s.pdfSelections||={})[target]={...clone(r.data),selected:choice?.selected??r.data.default_version,warned:old?.warned||null};
  await this.persist(s);return clone(s.pdfSelections[target]);
 });}
 async selectVersion(target,version){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const selection=s.pdfSelections?.[target];
  if(!Number.isSafeInteger(version)||!selection?.versions?.some(v=>v.id===version))throw Error('exact_version_required');
  const doc=s.documents[target];
  (s.pdfChoices||={})[target]={kind:s.commercialKind,document:doc.legacy_id,job:doc.job_id,customer:doc.customer_id,selected:version};
  selection.selected=version;selection.warned=null;await this.persist(s);return {version_id:version};
 });}
 async download(target,{confirmVersion}={}){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const doc=s.documents?.[target],selection=s.pdfSelections?.[target],version=selection?.selected;
  if(!doc||!Number.isSafeInteger(version)||version<=0)throw Error('exact_version_required');
  if(confirmVersion!==undefined&&(confirmVersion!==version||selection.warned!==version))throw Error('stale_confirmation_required');
  await this.policy(s);
  const r=await this.contact(s,'/'+s.commercialKind+'s/'+doc.legacy_id+'/pdf/versions/'+version+(confirmVersion===version?'?confirm_stale=1':''));
  if(r.status===409&&r.error?.code==='stale_pdf'){
   selection.warned=version;await this.persist(s);return {code:'stale_pdf',version_id:version,warning:'This exact PDF is stale. Confirm this version or explicitly select another.'};
  }
  if(r.status!==200)return this.failedPDFRead(s,target,r,'download_unavailable');
  // Bytes go directly to the caller, never to the protected persistent cache.
  return {version_id:version,bytes:r.bytes};
 });}
 async failedPDFRead(s,target,response,fallback){
  const doc=s.documents?.[target],kind=s.commercialKind;
  // A missing exact version or a gateway failure is not proof of parent loss.
  // Lock observations durably before verification; failed storage quarantines
  // the previous ciphertext across restart through the shared store lifecycle.
  await this.withholdCache(s);
  const notFound=r=>r.status===404&&r.error?.status===404&&r.error.code==='not_found'&&typeof r.error.request_id==='string'&&r.error.request_id.length>0;
  if(doc&&notFound(response)){
   const current=await this.contact(s,'/'+kind+'s/'+doc.legacy_id);
   if(notFound(current)){
    for(const [id,e] of Object.entries(s.pending)){
     if(e.kind===kind&&e.target===target)s.pending[id]=this.purgedOperation(e);
    }
    if(s.pdfChoices)delete s.pdfChoices[target];
    delete s.pdfDownloadWarning;
    await this.persistAuthorityLoss(s);
   }
  }
  throw Error(response.error?.code||fallback);
 }
 retainedPath(kind,document){
  if(!['invoice','estimate'].includes(kind)||!Number.isSafeInteger(document)||document<=0)throw Error('exact_document_required');
  return '/'+kind+'s/'+document+'/pdf/versions';
 }
 // Online retained-version access (including archived documents) depends on
 // current server Download/archived visibility, never on Generate or a lease.
 // Only one safe exact-version warning is retained, not document data or bytes.
 async retainedVersions(kind,document){return this.transaction(async s=>{
  const path=this.retainedPath(kind,document);
  const r=await this.contact(s,path);
  await this.receiptContactComplete(s);await this.policy(s,true);
  if(r.status!==200)throw Error(r.error?.code||'versions_unavailable');
  return clone(r.data);
 });}
 async downloadRetained(kind,document,version,{confirmVersion}={}){return this.transaction(async s=>{
  const path=this.retainedPath(kind,document);
  if(!Number.isSafeInteger(version)||version<=0)throw Error('exact_version_required');
  const identity=kind+':'+document+':'+version;
  if(confirmVersion!==undefined&&(confirmVersion!==version||s.pdfDownloadWarning!==identity))throw Error('stale_confirmation_required');
  const r=await this.contact(s,path+'/'+version+(confirmVersion===version?'?confirm_stale=1':''));
  await this.receiptContactComplete(s);await this.policy(s,true);
  if(r.status===409&&r.error?.code==='stale_pdf'){
   s.pdfDownloadWarning=identity;await this.persist(s);
   return {code:'stale_pdf',version_id:version,warning:'This exact PDF is stale. Confirm this version or explicitly select another.'};
  }
  if(r.status!==200)throw Error(r.error?.code||'download_unavailable');
  delete s.pdfDownloadWarning;await this.persist(s);return {version_id:version,bytes:r.bytes};
 });}
 async acceptedVersion(id){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const e=s.pending[id];
  if(e?.family!=='pdf'||e.receipt?.outcome!=='accepted'||!s.documents?.[e.target])throw Error('review_required');
  return {version_id:e.receipt.version_id};
 });}
}

// Legacy synchronous in-memory harness compatibility. New durable integrations
// use PDFClient above; this adapter cannot claim protected offline persistence.
class PDFReference {
  #request;
  #context;
  #records = new Map();
  #locked = false;
  #generation = 0;
  constructor(request, context) {
    // The transport supplies its credential identity and the authenticated
    // X-FreeFSM-Session-Epoch header on every response. No bearer is retained.
    if (typeof context?.client !== 'string' || !context.client || typeof context?.epoch !== 'string' || !context.epoch) throw new Error('authenticated_context_required');
    this.#context = Object.freeze({client: context.client, epoch: context.epoch});
    this.#request = (method, path, input) => request(method, path, input, this.#context);
  }
  stage(kind, review, dependencies = [], {occurredAt} = {}) {
    if (this.#locked) throw new Error('review_required');
    if (!['invoice', 'estimate'].includes(kind) || !Number.isSafeInteger(review.document_id) || review.document_id <= 0 || !Number.isSafeInteger(review.job_id) || review.job_id <= 0 || typeof review.source_token !== 'string' || !review.source_token || !Array.isArray(dependencies) || dependencies.some(value => typeof value !== 'string')) throw new Error('review_required');
    if (occurredAt !== undefined && (typeof occurredAt !== 'string' || !Number.isFinite(Date.parse(occurredAt)))) throw new Error('review_required');
    const intent = Object.freeze({operation_id: randomUUID(), job_id: review.job_id, source_token: review.source_token, depends_on: Object.freeze([...dependencies]), ...(occurredAt === undefined ? {} : {occurred_at: occurredAt})});
    const record = {kind, document: review.document_id, intent, state: 'pending', receipt: null, busy: false, terminalResponse: false};
    const entry = Object.freeze({kind, document: record.document, intent, get state() {return record.state;}, get receipt() {return record.receipt;}});
    this.#records.set(entry, record);
    return entry;
  }
  #record(entry) {
    const record = this.#records.get(entry);
    if (!record) throw new Error('review_required');
    if (record.busy) throw new Error('operation_pending');
    return record;
  }
  #lock() {
    this.#locked = true;
    ++this.#generation;
    for (const record of this.#records.values()) record.state = 'locked';
  }
  restoreDetected() { this.#lock(); }
  authenticationChanged() { this.#lock(); }
  #sameContext(response) {
    if ([401, 403].includes(response.status) || response.body?.error?.code === 'instance_restored' || response.client !== this.#context.client || response.epoch !== this.#context.epoch) {
      this.#lock(); return false;
    }
    return true;
  }
  #receipt(record, receipt) {
    if (!validReceipt(receipt,record.intent.operation_id)) {
      this.#lock(); return false;
    }
    record.receipt = Object.freeze({...receipt});
    record.state = receipt.outcome === 'accepted' ? 'accepted' : 'needs_review';
    return true;
  }
  async #inspect(record, generation) {
    const response = await this.#request('GET', `/pdf-operations/${record.intent.operation_id}`);
    if (generation !== this.#generation) return false;
    if (response.status >= 500 || response.status === 429) {record.state = 'unknown'; return false;}
    if (!this.#sameContext(response)) return false;
    if (response.status === 200) return this.#receipt(record, response.body?.data);
    if (response.status !== 404 || response.body?.error?.code !== 'not_found' || record.receipt) {this.#lock(); return false;}
    if (record.terminalResponse) {record.state = 'needs_review'; return false;}
    // Ordinary, authoritative receipt absence is necessary but insufficient:
    // recheck original source and current Generate authority without rewriting.
    const current = await this.#request('GET', `/${record.kind}s/${record.document}/pdf/source`);
    if (generation !== this.#generation) return false;
    if (current.status >= 500 || current.status === 429) {record.state = 'unknown'; return false;}
    if (!this.#sameContext(current)) return false;
    if (current.status !== 200) {this.#lock(); return false;}
    const source = current.body?.data;
    record.state = matchesSource(source,record.document,record.intent) ? 'retryable' : 'needs_review';
    return false;
  }
  async reconcile(entry) {
    const record = this.#record(entry);
    if (this.#locked || !['unknown', 'retryable'].includes(record.state)) throw new Error('review_required');
    record.busy = true;
    const generation = this.#generation;
    try { return await this.#inspect(record, generation); }
    catch { if (generation === this.#generation) record.state = 'unknown'; return false; }
    finally { record.busy = false; }
  }
  async submit(entry) {
    const record = this.#record(entry);
    if (this.#locked) throw new Error('review_required');
    if (record.state === 'unknown') { await this.reconcile(entry); return; }
    if (record.state !== 'pending') throw new Error('review_required');
    return this.#post(record);
  }
  async retryUnreceived(entry, {confirm = false} = {}) {
    const record = this.#record(entry);
    if (!confirm || this.#locked || record.state !== 'retryable' || record.receipt) throw new Error('review_required');
    // The caller explicitly reviewed unchanged retry. A receipt that appeared,
    // scope loss, source drift or epoch change still blocks any new POST.
    await this.reconcile(entry);
    if (record.state === 'retryable' && !this.#locked) return this.#post(record);
  }
  async #post(record) {
    record.busy = true;
    record.state = 'unknown';
    const generation = this.#generation;
    try {
      const response = await this.#request('POST', `/${record.kind}s/${record.document}/pdf/generate`, record.intent);
      if (generation !== this.#generation) return;
      if (response.status >= 500 || response.status === 429) return;
      if (!this.#sameContext(response)) return;
      if (response.status === 200) {this.#receipt(record, response.body?.data); return;}
      record.terminalResponse = true;
      await this.#inspect(record, generation);
    } catch { if (generation === this.#generation) record.state = 'unknown'; }
    finally { record.busy = false; }
  }
}
module.exports = { PDFClient, ProtectedStore, PDFReference };
