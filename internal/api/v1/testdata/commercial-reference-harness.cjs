'use strict';
const { randomUUID } = require('node:crypto');
const freeze = value => { if (value && typeof value === 'object') { Object.values(value).forEach(freeze); Object.freeze(value); } return value; };

// In-memory reference reconciliation, not a production encrypted mobile cache.
// Staging never contacts the server. No repair mutates an existing intent.
class CommercialReference {
  constructor(request) { this.request = request; }
  stageCreate(kind, job, values) {
    if (!['estimate', 'invoice'].includes(kind) || !Number.isSafeInteger(job) || job < 1) throw new Error('Job required; no Customer-only fallback');
    return this.entry(kind, 0, { ...values, job_id: job, client_id: randomUUID(), operation_id: randomUUID() });
  }
  stageEdit(kind, snapshot, values) {
    if (!snapshot.id || !snapshot.job_id || !snapshot.revision) throw new Error('Reviewed document required');
    return this.entry(kind, snapshot.id, { ...values, job_id: snapshot.job_id, revision: snapshot.revision, operation_id: randomUUID() });
  }
  entry(kind, target, intent) {
    return Object.defineProperties({ state: 'pending', receipt: null, unreceived: false }, {
      kind: { value: kind, enumerable: true }, target: { value: target, enumerable: true },
      intent: { value: freeze(JSON.parse(JSON.stringify(intent))), enumerable: true },
    });
  }
  async inspect(entry) {
    const response = await this.request('GET', `/commercial-operations/${entry.intent.operation_id}`);
    if (response.status === 200) {
      entry.receipt = response.body.data;
      entry.state = entry.receipt?.operation_id !== entry.intent.operation_id ? 'locked' : entry.receipt.outcome === 'accepted' ? 'accepted' : 'needs_review';
    } else if (response.status === 404) {
      entry.unreceived = !entry.receipt;
      entry.state = entry.receipt ? 'locked' : 'unknown';
    } else {
      entry.state = 'locked';
    }
    return entry;
  }
  async submit(entry) {
    if (entry.state !== 'pending') throw new Error('Inspect uncertain outcomes or explicitly review terminal intent first');
    entry.state = 'unknown';
    entry.unreceived = false;
    try {
      const response = await this.request(entry.target ? 'PATCH' : 'POST', `/${entry.kind}s${entry.target ? '/' + entry.target : ''}`, entry.intent);
      if (response.status === 200) {
        entry.receipt = response.body.data;
        entry.state = entry.receipt?.operation_id !== entry.intent.operation_id ? 'locked' : entry.receipt.outcome === 'accepted' ? 'accepted' : 'needs_review';
      } else {
        await this.inspect(entry);
      }
    } catch { /* Lost response retains original identity and uncertainty. */ }
    return entry;
  }
  retryUnreceived(entry) {
    if (entry.state !== 'unknown' || entry.receipt || !entry.unreceived) throw new Error('Only explicitly inspected unreceived intent may retry');
    entry.state = 'pending';
    return this.submit(entry);
  }
  reviewedEdit(entry, snapshot, values) {
    if (entry.state !== 'needs_review' || snapshot.id !== entry.target || snapshot.job_id !== entry.intent.job_id) throw new Error('Review must retain original target and Job');
    return this.stageEdit(entry.kind, snapshot, values);
  }
}
module.exports = { CommercialReference };
