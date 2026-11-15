'use strict';
const clone=value=>JSON.parse(JSON.stringify(value));

// Safe Payment recovery belongs to the shared pending-work lifecycle, regardless
// of which adapter observes authority loss. No financial values are retained here.
const uncertain=e=>e.family==='payment'&&(!!e.financialUncertain||['unknown','unreceived','locked_review','restore_uncertain'].includes(e.state));
function archiveReceipt(e){
 if(!e.receipt)return;
 e.historicalReceipts||=[];
 if(!e.historicalReceipts.some(r=>JSON.stringify(r)===JSON.stringify(e.receipt)))e.historicalReceipts.push(clone(e.receipt));
 delete e.receipt;
}
function purged(e,safe){
 if(e.family!=='payment')return safe;
 const restoreReview=e.restoreReview||(['locked_review','restore_uncertain'].includes(e.state)?{epoch:null}:undefined);
 return {...safe,generation:e.generation,historicalReceipts:e.historicalReceipts,restoreReview,financialUncertain:uncertain(e)};
}
function preserveGeneration(s){
 for(const e of Object.values(s.pending))if(e.family==='payment'&&!e.generation&&s.authority?.epoch)e.generation=s.authority.epoch;
}
function beginRestore(s){
 for(const e of Object.values(s.pending))if(e.family==='payment'){
  if(e.state!=='replaced')e.restoreReview={epoch:null};
  archiveReceipt(e);
 }
}
function authenticated(s,previous){
 for(const e of Object.values(s.pending))if(e.family==='payment'){
  const generation=e.generation||previous?.epoch;
  // An unversioned legacy receipt is historical until original-ID lookup.
  if(generation&&generation!==s.authority.epoch||!generation&&e.receipt){
   archiveReceipt(e);delete e.lookupMissing;delete e.reconciled;
   if(e.state!=='replaced'){e.restoreReview={epoch:null};e.state='locked_review';}
  }
  e.generation=s.authority.epoch;
 }
}
module.exports={uncertain,purged,preserveGeneration,beginRestore,authenticated};
