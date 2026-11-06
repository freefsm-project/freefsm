'use strict';
const {randomUUID}=require('node:crypto');
const {OfflineFieldClient,ProtectedStore}=require('./offline-notes-client.cjs');
const clone=x=>JSON.parse(JSON.stringify(x,(_key,value)=>{if(typeof value==='number'&&!Number.isFinite(value))throw Error('invalid_document');return value;}));
const kinds=new Set(['estimate','invoice']);
const itemFields=['id','legacy_id','name','type','description','sku','unit_price','taxable','tax_rate'];
const inputFields=['job_id','customer_id','title','notes','terms','tax_rate','invoice_date','due_date','valid_through','line_items','custom_fields','adjustments'];
const lineFields=['item_id','title','description','quantity','unit_price','taxable','tax_rate','discount','surcharge','discount_adjustment','surcharge_adjustment'];

// Explicit preparation, review, staging and submission are separate actions.
// The shared protected owner controls encryption, leases, purge and recovery.
class DocumentCreationClient extends OfflineFieldClient {
 async purge(s,issuance=null){delete s.items;delete s.itemCursor;return super.purge(s,issuance);}
 async withholdCache(s){s.items={};s.itemCursor=null;return super.withholdCache(s);}
 purgeSensitiveOperation(e){super.purgeSensitiveOperation(e);delete e.reviewedInputs;delete e.items;}
 async synchronize(delta=false){return this.transaction(async s=>{
  if(delta){await this.enforceOfflineAccessWindow(s);if(!s.syncCursor||!s.itemCursor)throw Error('synchronization_incomplete');}
  await this.policy(s);
  const jobs=delta?clone(s.cache):{},items=delta?clone(s.items||{}):{},jobCursor=delta?s.syncCursor:null,itemCursor=delta?s.itemCursor:null;
  await this.withholdCache(s);
  const jobPage=await this.pages(s,'/sync/jobs?scope=assigned',jobCursor,jobs);
  const itemPage=await this.pages(s,'/sync/items',itemCursor,items);
  const catalog={};
  for(const [id,item]of Object.entries(itemPage.records))catalog[id]=Object.fromEntries(itemFields.filter(k=>Object.hasOwn(item,k)).map(k=>[k,item[k]]));
  for(const e of Object.values(s.pending)){
   if(e.job&&!jobPage.records[e.job])this.purgeSensitiveOperation(e);
   else if(e.job&&e.family!=='document.create'&&!this.pendingTargetVisible(e,jobPage.records))this.purgeSensitiveOperation(e);
   else if(e.family==='document.create'&&e.intent){
    const inputs=jobPage.records[e.job]?.document_creation?.[e.kind];
    if(!inputs?.can_create||inputs.customer_id!==e.intent.customer_id)this.purgeSensitiveOperation(e);
    else {
     e.withheld=e.items?.some(item=>!Object.values(catalog).some(current=>current.id===item.id&&current.legacy_id===item.legacy_id&&typeof current.unit_price==='string'))||false;
     if(s.restore&&!['replaced','purged'].includes(e.state))e.state='restore_uncertain';
    }
   }else if(s.restore&&!['replaced','purged'].includes(e.state))e.state='restore_uncertain';
  }
  s.cache=jobPage.records;s.syncCursor=jobPage.cursor;s.items=catalog;s.itemCursor=itemPage.cursor;s.synced=true;s.restore=false;
  await this.persistAuthorityLoss(s);return {state:'synchronized',count:Object.keys(s.cache).length};
 });}
 async view(){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const pending=clone(s.pending);
  for(const e of Object.values(pending))if(e.withheld){delete e.intent;delete e.reviewedInputs;delete e.items;}
  return clone({cache:s.cache,items:s.items||{},pending});
 });}
 async selectItem(id,quantity){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const item=s.items?.[id];
  if(!item||typeof item.unit_price!=='string'||typeof quantity!=='string')throw Error('selection_unavailable');
  return {item_id:item.legacy_id,title:item.name,description:item.description||'',unit_price:item.unit_price,quantity,taxable:item.taxable};
 });}
 inputs(s,kind,job){const inputs=s.cache[job]?.document_creation?.[kind];if(!kinds.has(kind)||!inputs?.can_create)throw Error('review_required');return inputs;}
 async prepareCreation(kind,job,values){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const inputs=this.inputs(s,kind,job);
  const defaults={...inputs.company_defaults,...Object.fromEntries(Object.entries(inputs.customer_defaults).filter(([,value])=>value!==null))};
  const result={job_id:inputs.job_id,customer_id:inputs.customer_id,title:'',notes:defaults[kind+'_notes']||'',terms:defaults[kind+'_terms']||'',tax_rate:inputs.tax_rate,line_items:[],custom_fields:[],...clone(values)};
  result.line_items=result.line_items.map(line=>Object.hasOwn(line,'description')?line:{...line,description:defaults.line_description||''});
  if(kind==='invoice'){
   if(!Object.hasOwn(result,'invoice_date'))result.invoice_date=inputs.company_today;
   if(!Object.hasOwn(result,'due_date'))result.due_date=this.offsetDate(result.invoice_date,defaults.invoice_due_days);
  }else if(!Object.hasOwn(result,'valid_through'))result.valid_through=this.offsetDate(inputs.company_today,defaults.estimate_valid_days);
  for(const field of inputs.definitions||[])if(!field.retired&&field.default!==null&&!result.custom_fields.some(v=>v.definition_id===field.id))result.custom_fields.push({definition_id:field.id,value:field.type==='multiselect'?JSON.parse(field.default):field.default});
  return result;
 });}
 offsetDate(date,days){if(days===null||days===undefined||days==='')return '';const value=new Date(date+'T00:00:00Z');value.setUTCDate(value.getUTCDate()+Number(days));return value.toISOString().slice(0,10);}
 stageCreationIn(s,kind,job,reviewed){
  const inputs=this.inputs(s,kind,job);
  if(reviewed?.job_id!==inputs.job_id||reviewed.customer_id!==inputs.customer_id||typeof reviewed.title!=='string'||!reviewed.title.trim()||Object.keys(reviewed).some(k=>!inputFields.includes(k)))throw Error('review_required');
  // Defaults must have been resolved and reviewed before staging. An omitted
  // value must not acquire a different company/Customer default at submission.
  if(['notes','terms','tax_rate',...(kind==='invoice'?['invoice_date','due_date']:['valid_through'])].some(k=>typeof reviewed[k]!=='string')||!Array.isArray(reviewed.custom_fields)||!Array.isArray(reviewed.line_items))throw Error('review_required');
  const selected=[];
  for(const line of reviewed.line_items||[]){
   if(!line||typeof line.description!=='string'||Object.keys(line).some(k=>!lineFields.includes(k)))throw Error('review_required');
   if(!line.item_id)continue;
   const item=Object.values(s.items||{}).find(i=>i.legacy_id===line.item_id&&typeof i.unit_price==='string');
   if(!item)throw Error('selection_unavailable');selected.push(clone(item));
  }
  const id=randomUUID();s.pending[id]={family:'document.create',kind,job,state:'pending',intent:{...clone(reviewed),client_id:randomUUID(),operation_id:id},reviewedInputs:clone(inputs),items:selected};return id;
 }
 async stageCreation(kind,job,reviewed){return this.transaction(async s=>{await this.enforceOfflineAccessWindow(s);const id=this.stageCreationIn(s,kind,job,reviewed);await this.persist(s);return id;});}
 async repairCreation(id,reviewed){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const e=s.pending[id];
  if(e?.family!=='document.create'||!['terminal','restore_uncertain'].includes(e.state)||!e.intent)throw Error('review_required');
  if(e.state==='restore_uncertain'&&!e.reconciled)throw Error('restore_reconciliation_required');
  if(reviewed?.job_id!==e.intent.job_id||reviewed.customer_id!==e.intent.customer_id)throw Error('review_required');
  const next=this.stageCreationIn(s,e.kind,e.job,reviewed);e.state='replaced';await this.persist(s);return next;
 });}
 receiptPath(e,id){return e.family==='document.create'?'/commercial-operations/'+encodeURIComponent(id):super.receiptPath(e,id);}
 receipt(e,id,r){
  if(e.family!=='document.create')return super.receipt(e,id,r);
  if(r?.operation_id!==id||typeof r.outcome!=='string')throw Error('receipt_mismatch');
  e.receipt=clone(r);if(e.state!=='replaced')e.state=r.outcome==='accepted'?'accepted':'terminal';
  if(e.state==='accepted'){delete e.intent;delete e.reviewedInputs;delete e.items;}
 }
 async receiptAuthority(s,e,code,receiptOnly=false){
  if(e.family!=='document.create')return super.receiptAuthority(s,e,code,receiptOnly);
  code=e.receipt?.outcome||code;
  if(code==='forbidden'){await this.purge(s);return true;}
  if(code==='not_found'){
   // Target loss is narrower than session revocation. Purge this Job's private
   // work, retain unrelated intent and require fresh authorization for display.
   this.purgeSensitiveTarget(s,e.job);await this.withholdCache(s);return true;
  }
  if(code!=='accepted'){await this.withholdCache(s);return true;}
  return false;
 }
 async submitCreation(id){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const e=s.pending[id];
  if(e?.family!=='document.create'||e.state!=='pending'||!e.intent)throw Error('reconciliation_or_review_required');
  if(e.retryNotBefore&&this.now()<e.retryNotBefore)throw Error('retry_after_required');
  await this.policy(s);e.state='unknown';await this.persist(s);
  const r=await this.contact(s,'/'+e.kind+'s','POST',e.intent);
  if(r.status===200){this.receipt(e,id,r.data);await this.receiptAuthority(s,e,r.data.outcome);await this.persist(s);}
  else {
   if(r.status===429||r.status>=500)e.retryNotBefore=this.now()+this.retryDelay(r.error);
   await this.withholdCache(s);
  }
  return clone({state:e.state,receipt:e.receipt,status:r.status,code:r.error?.code});
 });}
}
module.exports={DocumentCreationClient,ProtectedStore};
