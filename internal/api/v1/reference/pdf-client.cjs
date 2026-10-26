'use strict';
const { randomUUID } = require('node:crypto');

// Executable reference state machine: offline staging stores intent, never PDF
// bytes or a local renderer. An application must persist this inside its own
// lease-bound protected store; this harness makes no durable-cache claim.
class PDFReference {
  constructor(request) { this.request = request; }
  stage(kind, review, dependencies = []) {
    if (!['invoice', 'estimate'].includes(kind) || !review.document_id || !review.job_id || !review.source_token) throw new Error('review_required');
    return { kind, document: review.document_id, state: 'pending', intent: Object.freeze({ operation_id: randomUUID(), job_id: review.job_id, source_token: review.source_token, depends_on: Object.freeze([...dependencies]) }) };
  }
  async reconcile(entry) {
    const result = await this.request('GET', `/pdf-operations/${entry.intent.operation_id}`);
    if (result.status === 404) return false;
    if (result.status !== 200) { entry.state = 'locked'; return false; }
    entry.receipt = result.body.data;
    entry.state = entry.receipt.outcome === 'accepted' ? 'accepted' : 'needs_review';
    return true;
  }
  async submit(entry) {
    if (entry.state === 'unknown') { await this.reconcile(entry); return; }
    if (entry.state !== 'pending') throw new Error('review_required');
    try {
      const result = await this.request('POST', `/${entry.kind}s/${entry.document}/pdf/generate`, entry.intent);
      if (result.status === 200) { entry.receipt = result.body.data; entry.state = 'accepted'; return; }
      if (result.status >= 500 || result.status === 429) { entry.state = 'unknown'; return; }
      entry.state = 'needs_review';
      await this.reconcile(entry);
    } catch (_) { entry.state = 'unknown'; }
  }
}
module.exports = { PDFReference };
