'use strict';
const {randomUUID}=require('node:crypto');

// A bounded executable reconciliation reference, not a production credential or
// offline-cache implementation. The caller owns current staging authorization.
class SubtaskReference {
  constructor(call, canStage) { this.call=call; this.canStage=canStage; this.pending=new Map(); }
  stage(jobID, subtask, completed) {
    if (!this.canStage()) throw new Error('staging_locked');
    if (!subtask.id || !subtask.completion_revision || typeof completed!=='boolean') throw new Error('invalid_intent');
    const operation=randomUUID();
    const record={jobID,subtaskID:subtask.id,body:{operation_id:operation,completion_revision:subtask.completion_revision,completed,occurred_at:new Date().toISOString()},state:'pending'};
    this.pending.set(operation,record);return operation;
  }
  async submit(operation) {
    const record=this.pending.get(operation);if (!record || record.state!=='pending') throw new Error('explicit_review_required');
    record.state='unknown';
    try {
      const response=await this.call('PATCH',`/jobs/${record.jobID}/subtasks/${record.subtaskID}`,record.body);
      if (response.status===200) {record.state='accepted';record.receipt=response.data;}
      else if (response.status>=400&&response.status<500&&response.status!==429) record.state='needs_attention';
      return response;
    } catch { return {status:0,code:'outcome_unknown'}; }
  }
  async reconcile(operation) {
    const record=this.pending.get(operation);if(!record)throw new Error('unknown_operation');
    const response=await this.call('GET',`/subtask-operations/${operation}`);
    if(response.status===200){record.receipt=response.data;record.state=response.data.code==='accepted'?'accepted':'needs_attention';}
    // A missing receipt or transport failure is not proof of nonacceptance.
    return response;
  }
  retrySameIntent(operation) {
    const record=this.pending.get(operation);if(!record||record.state!=='unknown')throw new Error('explicit_review_required');
    record.state='pending';return this.submit(operation);
  }
}
module.exports={SubtaskReference};
