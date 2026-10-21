'use strict';
const {randomUUID}=require('node:crypto');
const {ProtectedFieldClient,ProtectedStore}=require('./protected-field-client.cjs');
const clone=x=>JSON.parse(JSON.stringify(x));

// Clock workflows extend the accepted protected authority/receipt state machine.
// They use the same store, quarantine, expiry, restore and explicit batch retry.
class OfflineClocks extends ProtectedFieldClient {
 async batchFailure(s,ids,response){
  // Whole-envelope shape rejection is before every effect. It has no server
  // receipt, but permits explicit reviewed repair, unlike transport uncertainty.
  if(response.status===400&&['invalid_json','invalid_request'].includes(response.error?.code)){
   for(const id of ids){const e=s.pending[id];if(e.family==='clock'){e.state='terminal';e.code=response.error.code;}}
  }
 }
 receiptPath(e,id){return e.family==='clock'?`/time-operations/${encodeURIComponent(id)}`:super.receiptPath(e,id);}
 async receiptAuthority(s,e,code,receiptOnly=false){
  if(e.family!=='clock')return super.receiptAuthority(s,e,code,receiptOnly);
  if(!['forbidden','not_found'].includes(code))return false;
  // A GPS-only denial can be repaired without location, but never at the cost
  // of retaining a revoked Time/GPS projection. Refresh actual current scope.
  await this.synchronizeState(s,false);return true;
 }
 async clockLease(s){
  if(s.clockLease&&this.now()<Date.parse(s.clockLease.expires_at))return;
  const retry=s.clockIssuance?.state==='unknown';
  const id=retry?s.clockIssuance.id:randomUUID();
  s.clockIssuance={id,state:'unknown'};await this.persist(s);
  let r;
  if(retry){r=await this.contact(s,'/clock-lease-operations/'+id);if(r.status!==200&&r.status!==404)throw Error('clock_lease_unavailable');}
  if(!r||r.status===404)r=await this.contact(s,'/time-entries/lease','POST',{},id);
  if(r.status!==200||r.data?.code!=='accepted'||r.data.operation_id!==id||!r.data.id||!Number.isFinite(Date.parse(r.data.issued_at))||!Number.isFinite(Date.parse(r.data.expires_at)))throw Error('clock_lease_unavailable');
  s.clockLease=r.data;s.clockIssuance={id,state:'accepted'};await this.persist(s);
 }
 async synchronize(delta=false){return this.transaction(s=>this.synchronizeState(s,true,delta));}
 async synchronizeState(s,issueLease=true,delta=false){
  if(delta){await this.enforceOfflineAccessWindow(s);if(!s.clockCursor)throw Error('synchronization_incomplete');}
  const cursor=delta?s.clockCursor:null,previous=delta?clone(s.clocks):{};
  await this.policy(s);s.synced=false;s.clocks={};s.cache={};s.notes={};s.clockJobs={};await this.persistAuthorityLoss(s);
  const caps=await this.contact(s,'/capabilities');
  if(caps.status!==200||!caps.data?.clocks)throw Error('synchronization_incomplete');
  const snapshot=await this.pages(s,'/sync/clocks',cursor,previous),clocks=snapshot.records;
  const jobs=caps.data.sync?.available?(await this.pages(s,'/sync/jobs')).records:{};
  const clockJobs={};for(const job of Object.values(jobs))if(job.clock_job_id)clockJobs[job.clock_job_id]=true;
  const entries=new Set(Object.values(clocks).map(e=>e.entry_id));
  for(const e of Object.values(s.pending)){
   if(e.family!=='clock')continue;
   const command=e.intent?.clock;
   const job=command?.job_ref??command?.job_id,entry=command?.entry_ref??command?.entry_id;
   if(command&&((e.batch.type==='clock.in'&&job&&!clockJobs[String(job)])||(e.batch.type==='clock.out'&&!entries.has(String(entry))))){delete e.intent;delete e.batch;e.state='purged';}
   else if(s.restore&&!['replaced','purged'].includes(e.state))e.state='restore_uncertain';
  }
  s.clocks=clocks;s.clockJobs=clockJobs;s.clockCapabilities=caps.data.clocks;
  if(issueLease&&(caps.data.clocks.clock_in||caps.data.clocks.clock_out))await this.clockLease(s);
  s.clockCursor=snapshot.cursor;s.synced=true;s.restore=false;await this.persistAuthorityLoss(s);
  return {state:'synchronized',count:Object.keys(clocks).length};
 }
 async view(){return this.transaction(async s=>{await this.enforceOfflineAccessWindow(s);return clone({clocks:s.clocks||{},pending:s.pending,capabilities:s.clockCapabilities});});}
 async stageClock(action,reviewed,dependencies){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);
  if(!['in','out'].includes(action)||!s.clockCapabilities?.['clock_'+action]||!s.clockLease||this.now()>=Date.parse(s.clockLease.expires_at))throw Error('review_required');
  const keys=action==='in'?['job_id','notes','gps']:['entry_id','gps'];
  if(!reviewed||Object.keys(reviewed).some(k=>!keys.includes(k)))throw Error('review_required');
  for(const key of ['job_id','entry_id'])if(reviewed[key]!==undefined){
   const value=reviewed[key];
   if(typeof value==='string'){if(!/^[1-9][0-9]*$/.test(value)||value.length>19||BigInt(value)>9223372036854775807n)throw Error('review_required');}
   else if(!Number.isSafeInteger(value)||value<=0)throw Error('review_required');
  }
  if(action==='in'&&reviewed.job_id&&!s.clockJobs?.[String(reviewed.job_id)])throw Error('inaccessible_target');
  if(action==='out'&&!Object.values(s.clocks).some(e=>e.entry_id===String(reviewed.entry_id)&&e.capabilities.clock_out))throw Error('review_required');
  const command=clone(reviewed);
  for(const key of ['job_id','entry_id'])if(typeof command[key]==='string'){command[key.replace('_id','_ref')]=command[key];delete command[key];}
  const id=randomUUID();s.pending[id]={family:'clock',state:'pending',intent:{operation_id:id,clock:{...command,occurred_at:new Date(this.now()).toISOString(),lease_id:s.clockLease.id}},batch:{type:'clock.'+action,depends_on:this.reviewedDependencies(dependencies)}};
  await this.persist(s);return id;
 });}
 async submitWithoutLocation(id,dependencies){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const e=s.pending[id];
  if(e?.family!=='clock'||e.state!=='terminal'||!Object.prototype.hasOwnProperty.call(e.intent?.clock||{},'gps')||!['invalid_gps','forbidden','gps_immutable','invalid_json','invalid_request'].includes(e.receipt?.code||e.code))throw Error('review_required');
  const next=randomUUID(),intent=clone(e.intent);intent.operation_id=next;delete intent.clock.gps;
  s.pending[next]={family:'clock',state:'pending',intent,batch:{...clone(e.batch),depends_on:this.reviewedDependencies(dependencies)}};
  e.state='replaced';await this.persist(s);return next;
 });}
 async repairDependencies(id,dependencies){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);const e=s.pending[id];
  if(e?.family!=='clock'||e.state!=='terminal'||!e.intent||!['invalid_dependency','failed_dependency'].includes(e.receipt?.code))throw Error('review_required');
  const next=randomUUID(),intent=clone(e.intent);intent.operation_id=next;
  s.pending[next]={family:'clock',state:'pending',intent,batch:{...clone(e.batch),depends_on:this.reviewedDependencies(dependencies)}};
  e.state='replaced';await this.persist(s);return next;
 });}
}
module.exports={OfflineClocks,ProtectedStore};
