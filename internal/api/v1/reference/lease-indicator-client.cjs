'use strict';
const {OfflineFieldClient,ProtectedStore}=require('./offline-notes-client.cjs');

// Executable reference only. The observation lives inside the protected Job
// cache, so existing logout, expiry, restore and synchronization purge rules
// apply. No commercial dates or Lease references are stored or inferred.
class FieldLeaseClient extends OfflineFieldClient {
 async pages(...args){
  const snapshot=await super.pages(...args);
  // A sync commits only observations in its opaque identity space, including
  // when an unchanged delta record was inherited from the previous cache.
  for(const record of Object.values(snapshot.records))delete record.lease_indicators;
  return snapshot;
 }
 invalidateLeaseObservations(record){
  // Direct reads use numeric Asset IDs; synchronized projections use opaque IDs.
  // Invalidate the entire Job observation set without guessing identity mappings
  // or deleting authorized Asset/Service Entry facts.
  delete record.lease_indicators;
  for(const asset of [record.masters?.asset,...(record.masters?.assets||[])]){
   if(!asset)continue;
   delete asset.lease_coverage;
   for(const entry of asset.service_entries||[])delete entry.lease_coverage;
  }
 }
 async readLeaseIndicators(job){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);
  if(!s.cache[job])throw Error('inaccessible_target');
  // A refresh invalidates the saved observation before any network prerequisite,
  // including policy. Failed durable invalidation quarantines the store; ordinary
  // offline display without a refresh still retains its last synced observation.
   this.invalidateLeaseObservations(s.cache[job]);await this.persistAuthorityLoss(s);
  await this.policy(s);
  if(!s.cache[job])throw Error('inaccessible_target');
  const r=await this.contact(s,`/jobs/${encodeURIComponent(job)}/master-context`);
  if(r.status===404){this.purgeSensitiveTarget(s,job);await this.persistAuthorityLoss(s);throw Error('inaccessible_target');}
  if(r.status!==200||!r.data||typeof r.data!=='object')throw Error('indicator_unavailable');
  const values={};
  for(const asset of r.data.assets||[]){
   const c=asset.lease_coverage;
   if(!c||typeof c.covered!=='boolean'||typeof c.as_of!=='string'||!Number.isFinite(Date.parse(c.as_of)))throw Error('indicator_unavailable');
   values[String(asset.id)]={covered:c.covered,as_of:c.as_of};
  }
  s.cache[job].lease_indicators=values;await this.persist(s);
 });}
 async leaseIndicator(job,asset){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);
   const record=s.cache[job];
   const c=record?.lease_indicators?.[String(asset)]||record?.masters?.assets?.find(a=>a.id===asset)?.lease_coverage;
  if(!c)return {value:'Unavailable',label:'Lease coverage unavailable'};
  const value=c.covered?'Yes':'No';
  return {value,as_of:c.as_of,label:`Last synced: ${value} — as of ${c.as_of}`};
 });}
}
module.exports={FieldLeaseClient,ProtectedStore};
