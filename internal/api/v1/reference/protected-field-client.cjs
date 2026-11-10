'use strict';
const {randomUUID,createHash}=require('node:crypto');
const {ProtectedStore}=require('./protected-store.cjs');
const clone=x=>JSON.parse(JSON.stringify(x));
const clientIdentity=token=>token?createHash('sha256').update(token).digest('hex'):null;
const fresh=()=>({version:1,user:null,token:null,lease:null,authority:null,contactEpoch:null,issuance:null,leaseLocked:false,cache:{},notes:{},clocks:{},clockJobs:{},clockCapabilities:null,clockLease:null,clockIssuance:null,pending:{},syncCursor:null,clockCursor:null,synced:false,restore:false,highWater:0});

// Each action is explicit and serialized, including its network round trips.
// Nothing in construction/restart, authentication or synchronization submits work.
class ProtectedFieldClient {
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
  const safe={};for(const [id,e]of Object.entries(s.pending))safe[id]=this.purgedOperation(e);
  Object.assign(s,fresh(),{pending:safe,user:s.user,issuance});await this.persistAuthorityLoss(s);
 }
 purgedOperation(e){return {state:'purged',receipt:e.receipt,family:e.family,receipt_family:e.receipt_family};}
 async purgeDeniedIssuance(s,receipt){
  // A recovered terminal denial is ordinary authority loss, just like its 403.
  // Keep only the original issuance identity/disposition, never prior authority.
  await this.purge(s,{id:receipt.operation_id,state:'terminal',code:receipt.code});
  throw Error('authorization_purged');
 }
 beginRestore(s){
  // Missing-receipt lookup and its acknowledgement describe one server history.
  // Neither can authorize repair after another restore, even across restart.
  s.restore=true;
  for(const e of Object.values(s.pending)){
   delete e.lookupMissing;delete e.reconciled;
   if(e.state!=='replaced')e.state='locked_review';
  }
 }
 async contact(s,path,method='GET',body,operation){
  if(!s.token)throw Error('authentication_required');
  const r=await this.transport(path,method,body,{token:s.token,epoch:s.contactEpoch||s.lease?.epoch,operation});
  // Inspect disposition before generic 401 purge. Persist before returning data.
  if(r.error?.code==='instance_restored'&&r.error?.recovery_disposition==='locked_review'){
   s.token=null;s.lease=null;s.contactEpoch=null;s.restore=true;s.synced=false;s.cache={};s.notes={};s.clocks={};s.clockJobs={};s.clockCapabilities=null;s.clockLease=null;s.clockIssuance=null;s.clockCursor=null;
    this.beginRestore(s);
   await this.persistAuthorityLoss(s);throw Error('locked_review');
  }
  if(r.status===401||r.status===403){await this.purge(s);throw Error('authorization_purged');}
  if(r.error?.recovery_disposition==='purge_and_resnapshot'){
   // Ordinary scope uncertainty is not restore recovery: discard sensitive work.
   await this.purge(s);throw Error('authorization_purged');
  }
  return r;
 }
 // Traverse one protected projection; adapters own its cache commit and pending
 // intent policy. A delta starts from the caller's detached previous records.
 async pages(s,path,cursor=null,records={}){
  let page,completion;const seen=new Set();
  const query=path+(path.includes('?')?'&':'?')+'page_size=200';
  do{
   const r=await this.contact(s,query+(page?'&page='+encodeURIComponent(page):cursor?'&cursor='+encodeURIComponent(cursor):''));
   if(r.status!==200||!Array.isArray(r.data?.records)||typeof r.data.complete!=='boolean'||!['none','all'].includes(r.data.purge))throw Error('synchronization_incomplete');
   if(r.data.purge==='all'){await this.purge(s);throw Error('authorization_purged');}
   if(!Array.isArray(r.data.removed))throw Error('synchronization_incomplete');
   for(const id of r.data.removed)delete records[id];
   for(const record of r.data.records){if(typeof record?.id!=='string')throw Error('synchronization_incomplete');records[record.id]=record;}
   if(r.data.complete){if(typeof r.data.completion_cursor!=='string'||!r.data.completion_cursor||r.data.next_page!==null)throw Error('synchronization_incomplete');completion=r.data.completion_cursor;page=null;}
   else {page=r.data.next_page;if(!page||seen.has(page))throw Error('synchronization_incomplete');seen.add(page);}
  }while(page);
  return {records,cursor:completion};
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
   else if(previous&&previous.epoch!==l.epoch)this.beginRestore(s);
  else if(previous&&previous.client!==clientIdentity(token)&&!s.restore){
   // A new session at the same epoch is ordinary reauthorization, not restore.
   // Old-client intent cannot be rebound; keep only safe receipt identities.
   await this.purge(s);s.token=token;
  }
  s.clocks={};s.clockJobs={};s.clockCapabilities=null;s.clockLease=null;s.clockIssuance=null;s.clockCursor=null;
  s.authority={epoch:l.epoch,user:l.user_id,client:clientIdentity(token)};s.syncCursor=null;
  s.issuance={id:operation,state:'accepted'};s.user=l.user_id;s.lease=l;s.contactEpoch=l.epoch;s.leaseLocked=false;s.synced=false;s.cache={};s.notes={};s.highWater=Math.max(this.now(),issued);
  // Accepted issuance can reveal a restored epoch. Its review lock must survive
  // a failed final write, not just the earlier unknown-issuance checkpoint.
  await this.persistAuthorityLoss(s);return {expires_at:l.expires_at,state:'synchronization_required'};
 });}
 reviewedDependencies(dependencies){
   if(!Array.isArray(dependencies)||dependencies.some(id=>typeof id!=='string'||!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(id)))throw Error('review_required');
   return clone(dependencies);
  }
  async submitBatch(ids){return this.transaction(async s=>{
    await this.enforceOfflineAccessWindow(s);
    this.validateBatch(s);
   if(!Array.isArray(ids)||ids.length>100||new Set(ids).size!==ids.length)throw Error('batch_limit');
   const operations=ids.map(id=>{
    const e=s.pending[id];if(!e||e.state!=='pending'||!e.intent||!e.batch)throw Error('reconciliation_or_review_required');
    if(e.retryNotBefore&&this.now()<e.retryNotBefore)throw Error('retry_after_required');
     return this.batchOperation(e,id);
   });
   const body={operations};if(Buffer.byteLength(JSON.stringify(body),'utf8')>1048576)throw Error('batch_limit');
   await this.policy(s);
    for(const id of ids)s.pending[id].state='unknown';await this.batchCheckpoint(s);
   const r=await this.contact(s,'/sync/operations','POST',body);
   if(r.status===200){
    if(!Array.isArray(r.data?.results)||r.data.results.length!==ids.length||r.data.results.some((v,i)=>v.operation_id!==ids[i]||(v.receipt&&v.receipt.operation_id!==ids[i])))throw Error('receipt_mismatch');
    let lost=false;
    for(const result of r.data.results){
     const e=s.pending[result.operation_id];
     if(result.receipt){this.receipt(e,result.operation_id,result.receipt);if(await this.receiptAuthority(s,e,result.receipt.code))lost=true;}
     else if(result.error?.status===403||result.error?.status===401){await this.purge(s);throw Error('authorization_purged');}
     else if(result.error?.recovery_disposition==='retry')e.retryNotBefore=this.now()+this.retryDelay(result.error);
     // Without a terminal receipt retain unknown, including identity misuse.
    }
    if(lost)await this.persistAuthorityLoss(s);else await this.persist(s);
   }else{
    if(r.status===429||r.status>=500)for(const id of ids)s.pending[id].retryNotBefore=this.now()+this.retryDelay(r.error);
    await this.batchFailure(s,ids,r);
    await this.persist(s);
   }
   return ids.map(id=>({operation_id:id,state:s.pending[id].state,receipt:s.pending[id].receipt}));
  });}
  retryDelay(error){const seconds=error?.retry_after_seconds;return (Number.isSafeInteger(seconds)&&seconds>0&&seconds<Number.MAX_SAFE_INTEGER/1000?seconds:5)*1000;}
  validateBatch(s){}
  async batchCheckpoint(s){await this.persist(s);}
  batchOperation(e,id){return {...clone(e.intent),...clone(e.batch)};}
 async batchFailure(s,ids,response){}
 receipt(e,id,r){
  if(r?.operation_id!==id||!r.accepted_at||typeof r.code!=='string')throw Error('receipt_mismatch');
  e.receipt=r;if(e.state!=='replaced')e.state=r.code==='accepted'?'accepted':'terminal';if(e.state==='accepted')delete e.intent;
 }
 async reconcile(id){return this.transaction(async s=>{
  // Receipt lookup remains possible without resource visibility or a live offline
  // lease. It still requires a currently valid authenticated server session.
  if(!s.pending[id])throw Error('unknown_operation');
   let r;
   try{r=await this.contact(s,this.receiptPath(s.pending[id],id));}
   finally{await this.receiptContactComplete(s);}
  // Receipt access is independent of offline policy and target visibility. Check
  // policy on this reconnect, then apply only safe receipt metadata after purge.
  await this.policy(s,true);
  const e=s.pending[id];
  if(r.status===200){this.receipt(e,id,r.data);await this.receiptAuthority(s,e,r.data.code,true);}
  else if(r.status===404&&r.error?.code==='not_found'){
    this.receiptMissing(e);
  }
  // restore_uncertain + missing receipt remains uncertain, never retryable.
   await this.persist(s);return clone({state:e.state,receipt:e.receipt,requires_synchronization:!s.synced});
  });}
  // Domain projections may require fresh synchronization after scope-independent
  // receipt contact, including transport failure. Persist that lock before return.
  async receiptContactComplete(s){}
  receiptMissing(e){if(e.state==='unknown')e.state='pending';if(e.state==='restore_uncertain')e.lookupMissing=true;}
 receiptPath(){throw Error('unknown_operation');}
 async receiptAuthority(s,e,code){
  if(!['forbidden','not_found'].includes(code))return false;
  await this.purge(s);return true;
 }
 async acknowledgeUncertain(id){return this.transaction(async s=>{await this.enforceOfflineAccessWindow(s);const e=s.pending[id];if(!e||e.state!=='restore_uncertain'||!e.lookupMissing)throw Error('restore_reconciliation_required');e.reconciled=true;await this.persist(s);});}
 async discard(id){return this.transaction(async s=>{delete s.pending[id];await this.persist(s);});}
 async logout(){return this.store.exclusive(()=>this.store.destroy());}
}
module.exports={ProtectedFieldClient,ProtectedStore};
