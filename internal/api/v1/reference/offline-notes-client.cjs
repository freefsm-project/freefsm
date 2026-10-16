'use strict';
const {randomUUID,createHash}=require('node:crypto');
const {ProtectedStore}=require('./protected-store.cjs');
const clone=x=>JSON.parse(JSON.stringify(x));
const clientIdentity=token=>token?createHash('sha256').update(token).digest('hex'):null;
const fresh=()=>({version:1,user:null,token:null,lease:null,authority:null,contactEpoch:null,issuance:null,leaseLocked:false,cache:{},notes:{},pending:{},synced:false,restore:false,highWater:0});

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
 // Durably locks observed expiry/clock rollback and advances the high-water mark.
 async enforceOfflineAccessWindow(s){
  if(s.restore||!s.synced)throw Error('locked_review');
  const now=this.now();
  if(s.leaseLocked||!s.lease||!Number.isFinite(now)||now<s.highWater||now>=Date.parse(s.lease.expires_at)){
   s.leaseLocked=true;await this.persistAuthorityLoss(s);throw Error('authorization_lease_expired');
  }
  s.highWater=now;await this.persist(s);
 }
 async purge(s,issuance=null){
  // Retain only nonsensitive operation identities/dispositions for safe lookup.
   const safe={};for(const [id,e]of Object.entries(s.pending))safe[id]={state:'purged',receipt:e.receipt,...(e.family?{family:e.family}:{})};
  Object.assign(s,fresh(),{pending:safe,user:s.user,issuance});await this.persistAuthorityLoss(s);
 }
 async purgeDeniedIssuance(s,receipt){
  // A recovered terminal denial is ordinary authority loss, just like its 403.
  // Keep only the original issuance identity/disposition, never prior authority.
  await this.purge(s,{id:receipt.operation_id,state:'terminal',code:receipt.code});
  throw Error('authorization_purged');
 }
 purgeSensitiveTarget(s,job){
  delete s.cache[job];delete s.notes[job];
  for(const e of Object.values(s.pending))if(e.job===job){
   delete e.intent;delete e.batch;delete e.job;e.state='purged';
  }
 }
 async contact(s,path,method='GET',body,operation){
  if(!s.token)throw Error('authentication_required');
  const r=await this.transport(path,method,body,{token:s.token,epoch:s.contactEpoch||s.lease?.epoch,operation});
  // Inspect disposition before generic 401 purge. Persist before returning data.
  if(r.error?.code==='instance_restored'&&r.error?.recovery_disposition==='locked_review'){
   s.token=null;s.lease=null;s.contactEpoch=null;s.restore=true;s.synced=false;s.cache={};s.notes={};
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
 async policy(s,receiptOnly=false){
  const r=await this.contact(s,'/offline/policy');
  if(r.status!==200)throw Error('reauthorization_unavailable');
  if(!r.data?.enabled){await this.purge(s);if(!receiptOnly)throw Error('authorization_purged');}
 }
 async authenticate(token){return this.transaction(async s=>{
  if(typeof token!=='string'||!token)throw Error('authentication_required');
  const old=s.lease,oldUser=s.user,oldToken=s.token;
  const retry=oldToken===token&&s.issuance?.state==='unknown';
  const operation=retry?s.issuance.id:randomUUID();
  // Previous server authority and client binding outlive an interrupted issuance.
  // The new bearer alone is not evidence that old intent belongs to its epoch.
  const previous=retry?s.issuance.previous:(s.authority||(old?{epoch:old.epoch,user:oldUser,client:clientIdentity(oldToken)}:null));
  // Renewal must retain the last server epoch even while its lease is absent.
  s.contactEpoch=oldToken===token?(s.contactEpoch||old?.epoch):null;
  s.token=token;s.lease=null;s.synced=false;
  s.issuance={id:operation,state:'unknown',previous};await this.persist(s);
  let r;
  if(retry){
   r=await this.contact(s,'/offline-authorization-operations/'+operation);
   if(r.status!==200&&!(r.status===404&&r.error?.code==='not_found'))throw Error('reauthorization_unavailable');
  }
  if(!r||r.status===404)r=await this.contact(s,'/offline/lease','POST',{},operation);
  if(r.status!==200){s.token=null;await this.persist(s);throw Error('reauthorization_unavailable');}
  const l=r.data,issued=Date.parse(l?.issued_at),expires=Date.parse(l?.expires_at);
  if(l?.operation_id===operation&&['offline_disabled','forbidden'].includes(l.code))await this.purgeDeniedIssuance(s,l);
  if(l?.code!=='accepted'||l.operation_id!==operation||!l.id||!l.user_id||!l.epoch||!Number.isFinite(issued)||!Number.isFinite(expires)||expires<=issued||expires-issued>72*3600000||this.now()>=expires){s.token=null;await this.persist(s);throw Error('invalid_lease');}
  if(oldUser&&oldUser!==l.user_id){await this.purge(s);s.pending={};s.token=token;}
  else if(previous&&previous.epoch!==l.epoch)s.restore=true;
  else if(previous&&previous.client!==clientIdentity(token)&&!s.restore){
   // A new session at the same epoch is ordinary reauthorization, not restore.
   // Old-client intent cannot be rebound; keep only safe receipt identities.
   await this.purge(s);s.token=token;
  }
  s.authority={epoch:l.epoch,user:l.user_id,client:clientIdentity(token)};
  s.issuance={id:operation,state:'accepted'};s.user=l.user_id;s.lease=l;s.contactEpoch=l.epoch;s.leaseLocked=false;s.synced=false;s.cache={};s.notes={};s.highWater=Math.max(this.now(),issued);
  // Accepted issuance can reveal a restored epoch. Its review lock must survive
  // a failed final write, not just the earlier unknown-issuance checkpoint.
  await this.persistAuthorityLoss(s);return {expires_at:l.expires_at,state:'synchronization_required'};
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
    if(e.job&&!this.pendingTargetVisible(e,records))this.purgeSensitiveTarget(s,e.job);
   else if(s.restore&&!['replaced','purged'].includes(e.state))e.state='restore_uncertain';
  }
  // The complete snapshot carries observed scope removals and restore outcomes.
  // Failed persistence must not revive older intent after authority returns.
  s.cache=records;s.synced=true;s.restore=false;await this.persistAuthorityLoss(s);
  return {state:'synchronized',count:Object.keys(records).length};
 });}
  pendingTargetVisible(e,records){return !!records[e.job];}
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
 reviewedDependencies(dependencies){
   if(!Array.isArray(dependencies)||dependencies.some(id=>typeof id!=='string'||!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(id)))throw Error('review_required');
   return clone(dependencies);
  }
 async stageBatch(job,text,dependencies){return this.transaction(async s=>{
   await this.enforceOfflineAccessWindow(s);const depends_on=this.reviewedDependencies(dependencies);
   const id=this.stageIn(s,job,text);s.pending[id].batch={type:'job.field_notes',target:job,depends_on};await this.persist(s);return id;
  });}
 async submitBatch(ids){return this.transaction(async s=>{
   await this.enforceOfflineAccessWindow(s);
   if(!Array.isArray(ids)||ids.length>100||new Set(ids).size!==ids.length)throw Error('batch_limit');
   const operations=ids.map(id=>{
    const e=s.pending[id];if(!e||e.state!=='pending'||!e.intent||!e.batch)throw Error('reconciliation_or_review_required');
    if(e.retryNotBefore&&this.now()<e.retryNotBefore)throw Error('retry_after_required');
    return {...clone(e.intent),...clone(e.batch)};
   });
   const body={operations};if(Buffer.byteLength(JSON.stringify(body),'utf8')>1048576)throw Error('batch_limit');
   await this.policy(s);
   for(const id of ids)s.pending[id].state='unknown';await this.persist(s);
   const r=await this.contact(s,'/sync/operations','POST',body);
   if(r.status===200){
    if(!Array.isArray(r.data?.results)||r.data.results.length!==ids.length||r.data.results.some((v,i)=>v.operation_id!==ids[i]||(v.receipt&&v.receipt.operation_id!==ids[i])))throw Error('receipt_mismatch');
    let lost=false;
    for(const result of r.data.results){
     const e=s.pending[result.operation_id];
     if(result.receipt){this.receipt(e,result.operation_id,result.receipt);if(['forbidden','not_found'].includes(result.receipt.code)){this.purgeSensitiveTarget(s,e.job);lost=true;}}
     else if(result.error?.status===403||result.error?.status===401){await this.purge(s);throw Error('authorization_purged');}
     else if(result.error?.recovery_disposition==='retry')e.retryNotBefore=this.now()+this.retryDelay(result.error);
     // Without a terminal receipt retain unknown, including identity misuse.
    }
    if(lost)await this.persistAuthorityLoss(s);else await this.persist(s);
   }else{
    if(r.status===429||r.status>=500)for(const id of ids)s.pending[id].retryNotBefore=this.now()+this.retryDelay(r.error);
    await this.persist(s);
   }
   return ids.map(id=>({operation_id:id,state:s.pending[id].state,receipt:s.pending[id].receipt}));
  });}
 retryDelay(error){const seconds=error?.retry_after_seconds;return (Number.isSafeInteger(seconds)&&seconds>0&&seconds<Number.MAX_SAFE_INTEGER/1000?seconds:5)*1000;}
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
  else if(r.status===429||r.status>=500){
   e.retryNotBefore=this.now()+this.retryDelay(r.error);
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
  if(!s.pending[id])throw Error('unknown_operation');
   const r=await this.contact(s,`/${s.pending[id].family==='file'?'file-operations':'job-execution-operations'}/${encodeURIComponent(id)}`);
  // Receipt access is independent of offline policy and target visibility. Check
  // policy on this reconnect, then apply only safe receipt metadata after purge.
  await this.policy(s,true);
  const e=s.pending[id];
  if(r.status===200){this.receipt(e,id,r.data);if(['forbidden','not_found'].includes(r.data.code))await this.purge(s);}
  else if(r.status===404&&r.error?.code==='not_found'){
   if(e.state==='unknown')e.state='pending';
   if(e.state==='restore_uncertain')e.lookupMissing=true;
  }
  // restore_uncertain + missing receipt remains uncertain, never retryable.
  await this.persist(s);return clone({state:e.state,receipt:e.receipt});
 });}
 async repair(id,text,reviewedRevision,reviewedDependencies){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const e=s.pending[id];
  if(!e||!['terminal','restore_uncertain'].includes(e.state)||!e.job)throw Error('review_required');
  if(e.state==='restore_uncertain'&&!e.reconciled)throw Error('restore_reconciliation_required');
  if(!reviewedRevision||s.notes[e.job]?.notes_revision!==reviewedRevision)throw Error('review_required');
  const batch=e.batch?{type:e.batch.type,target:e.batch.target,depends_on:this.reviewedDependencies(reviewedDependencies)}:null;
  const next=this.stageIn(s,e.job,text);if(batch)s.pending[next].batch=batch;e.state='replaced';await this.persist(s);return next;
 });}
 async acknowledgeUncertain(id){return this.transaction(async s=>{await this.enforceOfflineAccessWindow(s);const e=s.pending[id];if(!e||e.state!=='restore_uncertain'||!e.lookupMissing)throw Error('restore_reconciliation_required');e.reconciled=true;await this.persist(s);});}
 async discard(id){return this.transaction(async s=>{delete s.pending[id];await this.persist(s);});}
 async logout(){return this.store.exclusive(()=>this.store.destroy());}
}
module.exports={OfflineNotes,ProtectedStore};
