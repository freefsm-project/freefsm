'use strict';
const {randomUUID}=require('node:crypto');
const {ProtectedStore}=require('./protected-store.cjs');
const clone=x=>JSON.parse(JSON.stringify(x));
const fresh=()=>({version:1,user:null,token:null,lease:null,leaseLocked:false,cache:{},notes:{},pending:{},synced:false,restore:false,highWater:0});

// Each action is explicit and serialized, including its network round trips.
// Nothing in construction/restart, authentication or synchronization submits work.
class OfflineNotes {
 constructor(store,transport,now=()=>Date.now()){this.store=store;this.transport=transport;this.now=now;}
 async initialize(){return this.store.exclusive(async()=>{await this.store.create();try{await this.store.load();}catch(e){if(e.code!=='ENOENT')throw e;await this.store.save(fresh());}});}
 async transaction(fn){return this.store.exclusive(async()=>{const s=await this.store.load();return fn(s);});}
 async persist(s){await this.store.save(s);}
 async persistAuthorityLoss(s){
  try{await this.persist(s);}catch{
   // The old ciphertext must not become visible after a failed purge/restore
   // write. The already-durable exclusive marker survives and blocks restart.
   this.store.quarantined=true;throw Error('protected_store_locked');
  }
 }
 async gate(s){
  if(s.restore||!s.synced)throw Error('locked_review');
  const now=this.now();
  if(s.leaseLocked||!s.lease||!Number.isFinite(now)||now<s.highWater||now>=Date.parse(s.lease.expires_at)){
   s.leaseLocked=true;await this.persistAuthorityLoss(s);throw Error('authorization_lease_expired');
  }
  s.highWater=now;await this.persist(s);
 }
 async purge(s){
  // Retain only nonsensitive operation identities/dispositions for safe lookup.
  const safe={};for(const [id,e]of Object.entries(s.pending))safe[id]={state:'purged',receipt:e.receipt};
  Object.assign(s,fresh(),{pending:safe,user:s.user});await this.persistAuthorityLoss(s);
 }
 async contact(s,path,method='GET',body,operation){
  if(!s.token)throw Error('authentication_required');
  const r=await this.transport(path,method,body,{token:s.token,epoch:s.lease?.epoch,operation});
  // Inspect disposition before generic 401 purge. Persist before returning data.
  if(r.error?.code==='instance_restored'&&r.error?.recovery_disposition==='locked_review'){
   s.token=null;s.lease=null;s.restore=true;s.synced=false;s.cache={};s.notes={};
   for(const e of Object.values(s.pending)){if(e.state!=='replaced')e.state='locked_review';}
   await this.persistAuthorityLoss(s);throw Error('locked_review');
  }
  if(r.status===401||r.status===403){await this.purge(s);throw Error('authorization_purged');}
  if(r.error?.recovery_disposition==='purge_and_resnapshot'){
   // Ordinary scope uncertainty is not restore recovery: discard sensitive work.
   await this.purge(s);throw Error('authorization_purged');
  }
  return r;
 }
 async policy(s){
  const r=await this.contact(s,'/offline/policy');
  if(r.status!==200)throw Error('reauthorization_unavailable');
  if(!r.data?.enabled){await this.purge(s);throw Error('authorization_purged');}
 }
 async authenticate(token){return this.transaction(async s=>{
  if(typeof token!=='string'||!token)throw Error('authentication_required');
  const old=s.lease,oldUser=s.user,oldToken=s.token;
  const retry=oldToken===token&&s.issuance?.state==='unknown';
  const operation=retry?s.issuance.id:randomUUID();
  s.token=token;s.lease=null;s.synced=false;
  s.issuance={id:operation,state:'unknown'};await this.persist(s);
  let r;
  if(retry){
   r=await this.contact(s,'/offline-authorization-operations/'+operation);
   if(r.status!==200&&!(r.status===404&&r.error?.code==='not_found'))throw Error('reauthorization_unavailable');
  }
  if(!r||r.status===404)r=await this.contact(s,'/offline/lease','POST',{},operation);
  if(r.status!==200){s.token=null;await this.persist(s);throw Error('reauthorization_unavailable');}
  const l=r.data,issued=Date.parse(l?.issued_at),expires=Date.parse(l?.expires_at);
  if(l?.code!=='accepted'||l.operation_id!==operation||!l.id||!l.user_id||!l.epoch||!Number.isFinite(issued)||!Number.isFinite(expires)||expires<=issued||expires-issued>72*3600000||this.now()>=expires){s.token=null;await this.persist(s);throw Error('invalid_lease');}
  if(oldUser&&oldUser!==l.user_id){await this.purge(s);s.pending={};s.token=token;}
  if(old&&old.epoch!==l.epoch||oldUser&&oldToken!==token)s.restore=true;
  s.issuance={id:operation,state:'accepted'};s.user=l.user_id;s.lease=l;s.leaseLocked=false;s.synced=false;s.cache={};s.notes={};s.highWater=Math.max(this.now(),issued);
  await this.persist(s);return {expires_at:l.expires_at,state:'synchronization_required'};
 });}
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
   if(e.job&&!records[e.job]){delete e.intent;delete e.job;e.state='purged';}
   else if(s.restore&&e.state!=='replaced')e.state='restore_uncertain';
  }
  s.cache=records;s.synced=true;s.restore=false;await this.persist(s);
  return {state:'synchronized',count:Object.keys(records).length};
 });}
 async readNotes(job){return this.transaction(async s=>{
  await this.gate(s);await this.policy(s);
  if(!s.cache[job])throw Error('inaccessible_target');
  const r=await this.contact(s,`/jobs/${encodeURIComponent(job)}/field-notes`);
  if(r.status===404){delete s.cache[job];delete s.notes[job];for(const e of Object.values(s.pending))if(e.job===job){delete e.intent;delete e.job;e.state='purged';}await this.persistAuthorityLoss(s);throw Error('inaccessible_target');}
  if(r.status!==200||typeof r.data?.notes_revision!=='string'||!r.data.notes_revision)throw Error('notes_unavailable');
  s.notes[job]=r.data;await this.persist(s);return clone(r.data);
 });}
 async view(){return this.transaction(async s=>{
  await this.gate(s);const pending=clone(s.pending);
  const labels={pending:'Pending — not accepted',unknown:'Save status unknown — check original receipt',accepted:'Accepted by server',terminal:'Needs attention',restore_uncertain:'Needs attention — restore outcome uncertain',purged:'Inaccessible content purged',replaced:'Replaced by explicitly reviewed intent'};
  for(const e of Object.values(pending))e.label=labels[e.state]||'Review required';
  return clone({cache:s.cache,notes:s.notes,pending});
 });}
 stageIn(s,job,text){
  if(!s.cache[job]||!s.notes[job]?.can_edit_notes||typeof text!=='string')throw Error('review_required');
  const id=randomUUID();s.pending[id]={job,state:'pending',intent:{operation_id:id,notes_revision:s.notes[job].notes_revision,field_notes:text,occurred_at:new Date(this.now()).toISOString()}};return id;
 }
 async stage(job,text){return this.transaction(async s=>{await this.gate(s);const id=this.stageIn(s,job,text);await this.persist(s);return id;});}
 async submit(id){return this.transaction(async s=>{
  await this.gate(s);const e=s.pending[id];
  if(!e||e.state!=='pending'||!e.intent)throw Error('reconciliation_or_review_required');
  if(e.retryNotBefore&&this.now()<e.retryNotBefore)throw Error('retry_after_required');
  await this.policy(s);
  // Crash/response loss after this durable write always requires receipt lookup.
  e.state='unknown';await this.persist(s);
  const r=await this.contact(s,`/jobs/${encodeURIComponent(e.job)}/field-notes`,'POST',e.intent);
  if(r.status===200){this.receipt(e,id,r.data);}
  else if([400,404,409].includes(r.status)){e.state='terminal';e.code=r.error?.code;if(r.status===404){delete e.intent;delete s.cache[e.job];delete s.notes[e.job];delete e.job;e.state='purged';}}
  else if(r.status===429||r.status>=500){
   const seconds=r.error?.retry_after_seconds;
   const delay=Number.isSafeInteger(seconds)&&seconds>0&&seconds<Number.MAX_SAFE_INTEGER/1000?seconds:5;
   e.retryNotBefore=this.now()+delay*1000;
  }
  // Even a service failure remains unknown until receipt lookup. Retry is explicit.
  if(r.status===404)await this.persistAuthorityLoss(s);else await this.persist(s);
  return {state:e.state,code:e.code,retry_not_before:e.retryNotBefore};
 });}
 receipt(e,id,r){
  if(r?.operation_id!==id||!r.accepted_at||typeof r.code!=='string')throw Error('receipt_mismatch');
  e.receipt=r;if(e.state!=='replaced')e.state=r.code==='accepted'?'accepted':'terminal';if(e.state==='accepted')delete e.intent;
 }
 async reconcile(id){return this.transaction(async s=>{
  // Receipt lookup remains possible without resource visibility or a live offline
  // lease. It still requires a currently valid authenticated server session.
  const e=s.pending[id];if(!e)throw Error('unknown_operation');
  const r=await this.contact(s,`/job-execution-operations/${encodeURIComponent(id)}`);
  if(r.status===200){this.receipt(e,id,r.data);if(['forbidden','not_found'].includes(r.data.code))await this.purge(s);}
  else if(r.status===404&&r.error?.code==='not_found'){
   if(e.state==='unknown')e.state='pending';
   if(e.state==='restore_uncertain')e.lookupMissing=true;
  }
  // restore_uncertain + missing receipt remains uncertain, never retryable.
  await this.persist(s);return clone({state:e.state,receipt:e.receipt});
 });}
 async repair(id,text,reviewedRevision){return this.transaction(async s=>{
  await this.gate(s);const e=s.pending[id];
  if(!e||!['terminal','restore_uncertain'].includes(e.state)||!e.job)throw Error('review_required');
  if(e.state==='restore_uncertain'&&!e.reconciled)throw Error('restore_reconciliation_required');
  if(!reviewedRevision||s.notes[e.job]?.notes_revision!==reviewedRevision)throw Error('review_required');
  const next=this.stageIn(s,e.job,text);e.state='replaced';await this.persist(s);return next;
 });}
 async acknowledgeUncertain(id){return this.transaction(async s=>{await this.gate(s);const e=s.pending[id];if(!e||e.state!=='restore_uncertain'||!e.lookupMissing)throw Error('restore_reconciliation_required');e.reconciled=true;await this.persist(s);});}
 async discard(id){return this.transaction(async s=>{delete s.pending[id];await this.persist(s);});}
 async logout(){return this.store.exclusive(()=>this.store.destroy());}
}
module.exports={OfflineNotes,ProtectedStore};
