// Executable reference state machine, not a production offline client.
// A platform client must additionally protect keys and cached data at rest.
class SessionReference {
 constructor() { this.epoch=''; this.cache=[]; this.pending=[]; this.state='signed_out'; this.reviewReady=false; }
 authorized(epoch) { this.epoch=epoch; this.reviewReady=true; this.state=this.pending.length?'locked_review':'online'; }
 stage(intent) { if(this.state!=='online')throw Error('fresh authorization required'); this.pending.push(intent); }
 contact(response) {
  const disposition=response.error?.recovery_disposition;
  if(disposition==='purge') { this.cache=[];this.pending=[];this.state='signed_out';this.reviewReady=false; }
  if(disposition==='locked_review') { this.cache=[];this.state='locked_review';this.reviewReady=false; }
 }
 review(currentAuthorizedIDs) {
  if(this.state!=='locked_review'||!this.reviewReady)throw Error('fresh authentication and current authority synchronization required');
  // Inaccessible cached content is never a recovery exception.
  this.pending=this.pending.filter(p=>currentAuthorizedIDs.includes(p.target));
  return this.pending.map(p=>({...p,requiresExplicitReconciliation:true}));
 }
 // Intentionally no replay/submit callback: review cannot send a mutation.
}
module.exports={SessionReference};
