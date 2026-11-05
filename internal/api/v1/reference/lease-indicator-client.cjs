'use strict';
const {OfflineFieldClient,ProtectedStore}=require('./offline-notes-client.cjs');

// Executable reference only. The observation lives inside the protected Job
// cache, so existing logout, expiry, restore and synchronization purge rules
// apply. No commercial dates or Lease references are stored or inferred.
class FieldLeaseClient extends OfflineFieldClient {
 async readLeaseIndicators(job){return this.transaction(async s=>{
  await this.enforceOfflineAccessWindow(s);
  // A refresh invalidates the saved observation before any network prerequisite,
  // including policy. Failed durable invalidation quarantines the store; ordinary
  // offline display without a refresh still retains its last synced observation.
  if(s.cache[job]){delete s.cache[job].lease_indicators;await this.persistAuthorityLoss(s);}
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
  const c=s.cache[job]?.lease_indicators?.[String(asset)];
  if(!c)return {value:'Unavailable',label:'Lease coverage unavailable'};
  const value=c.covered?'Yes':'No';
  return {value,as_of:c.as_of,label:`Last synced: ${value} — as of ${c.as_of}`};
 });}
}
module.exports={FieldLeaseClient,ProtectedStore};
