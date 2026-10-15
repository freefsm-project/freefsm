/* Executable in-memory offline reference for the two independent Job facets.
 * The host supplies authenticated transport and an unexpired authorization
 * lease. No bearer, record content or pending intent is persisted by this file.
 */
(function (root) {
  'use strict';
  class JobExecutionReference {
    constructor(call, leaseUntil, now = () => Date.now()) {
      this.call = call; this.leaseUntil = leaseUntil; this.now = now;
      this.pending = new Map();
    }
    stage(job, facet, reviewedIntent, operationID) {
      if (this.now() >= this.leaseUntil) throw new Error('authorization_lease_expired');
      if (!['status', 'field-notes'].includes(facet) || !operationID || this.pending.has(operationID)) throw new Error('invalid_staging');
      const intent = Object.freeze(JSON.parse(JSON.stringify({ ...reviewedIntent, operation_id: operationID })));
      if (facet === 'status' && (!intent.status_id || !intent.status_revision || !intent.meaning_revision)) throw new Error('review_required');
      if (facet === 'field-notes' && !intent.notes_revision) throw new Error('review_required');
      this.pending.set(operationID, { job, facet, intent, state: 'pending' });
      return operationID;
    }
    async reconcile(operationID) {
      const entry = this.pending.get(operationID);
      if (!entry) throw new Error('unknown_operation');
      const response = await this.call(`/job-execution-operations/${operationID}`, 'GET');
      if (response.error?.recovery_disposition === 'locked_review') {
        this.lockForRestore(); return null;
      }
      if (response.status === 200) {
        const receipt = response.data;
        if (receipt?.operation_id !== operationID || !receipt.accepted_at || typeof receipt.code !== 'string') throw new Error('receipt_mismatch');
        entry.state = receipt.code === 'accepted' ? 'accepted' : 'terminal';
        entry.receipt = receipt;
        if (receipt.code === 'accepted') delete entry.intent;
        if (['not_found', 'forbidden'].includes(receipt.code)) this.purge();
        return receipt;
      }
      if (response.status === 401 || response.status === 403) this.purge();
      if (response.status === 404 && response.error?.code === 'not_found' && entry.state === 'unknown') entry.state = 'pending';
      // Absence is not proof of failure. Never automatically repeat intent.
      return null;
    }
    async submit(operationID) {
      const entry = this.pending.get(operationID);
      if (entry?.state === 'unknown') throw new Error('reconciliation_required');
      if (!entry || entry.state !== 'pending' || !entry.intent) throw new Error('review_required');
      if (this.now() >= this.leaseUntil) throw new Error('authorization_lease_expired');
      // Mark uncertainty before transport: a lost response must be reconciled
      // before the caller may explicitly retry the unchanged operation.
      entry.state = 'unknown';
      const response = await this.call(`/jobs/${entry.job}/${entry.facet}`, 'POST', entry.intent);
      if (response.error?.recovery_disposition === 'locked_review') {
        this.lockForRestore();
      } else if (response.status === 200) {
        if (response.data?.operation_id !== operationID || !response.data.accepted_at || response.data.code !== 'accepted') throw new Error('receipt_mismatch');
        entry.state = 'accepted'; entry.receipt = response.data; delete entry.intent;
      } else if ([400, 401, 403, 404, 409].includes(response.status)) {
        entry.state = 'terminal';
        if ([401, 403, 404].includes(response.status)) this.purge();
      } else if (response.status === 429 || response.status >= 500) {
        entry.state = 'pending';
      }
      return response;
    }
    repair(originalID, job, facet, explicitlyReviewedIntent, newID) {
      const original = this.pending.get(originalID);
      if (!original || original.state !== 'terminal' || newID === originalID) throw new Error('review_required');
      return this.stage(job, facet, explicitlyReviewedIntent, newID);
    }
    discard(operationID) { this.pending.delete(operationID); }
    lockForRestore() {
      this.leaseUntil = 0;
      for (const entry of this.pending.values()) {
        if (entry.intent) entry.state = 'locked_review';
      }
    }
    purge() {
      for (const entry of this.pending.values()) {
        delete entry.intent; delete entry.job; delete entry.facet;
        if (['pending', 'unknown', 'locked_review'].includes(entry.state)) entry.state = 'terminal';
      }
    }
    logout() { this.purge(); this.pending.clear(); this.leaseUntil = 0; }
  }
  if (typeof module !== 'undefined') module.exports = { JobExecutionReference };
  else root.JobExecutionReference = JobExecutionReference;
})(globalThis);
