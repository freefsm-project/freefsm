'use strict';
const { randomUUID } = require('node:crypto');

// Executable reference state machine: offline staging stores intent, never PDF
// bytes or a local renderer. An application must persist this inside its own
// lease-bound protected store; this harness makes no durable-cache claim.
class PDFReference {
  #request;
  #context;
  #records = new Map();
  #locked = false;
  #generation = 0;
  constructor(request, context) {
    // The transport supplies its credential identity and the authenticated
    // X-FreeFSM-Session-Epoch header on every response. No bearer is retained.
    if (typeof context?.client !== 'string' || !context.client || typeof context?.epoch !== 'string' || !context.epoch) throw new Error('authenticated_context_required');
    this.#context = Object.freeze({client: context.client, epoch: context.epoch});
    this.#request = (method, path, input) => request(method, path, input, this.#context);
  }
  stage(kind, review, dependencies = [], {occurredAt} = {}) {
    if (this.#locked) throw new Error('review_required');
    if (!['invoice', 'estimate'].includes(kind) || !Number.isSafeInteger(review.document_id) || review.document_id <= 0 || !Number.isSafeInteger(review.job_id) || review.job_id <= 0 || typeof review.source_token !== 'string' || !review.source_token || !Array.isArray(dependencies) || dependencies.some(value => typeof value !== 'string')) throw new Error('review_required');
    if (occurredAt !== undefined && (typeof occurredAt !== 'string' || !Number.isFinite(Date.parse(occurredAt)))) throw new Error('review_required');
    const intent = Object.freeze({operation_id: randomUUID(), job_id: review.job_id, source_token: review.source_token, depends_on: Object.freeze([...dependencies]), ...(occurredAt === undefined ? {} : {occurred_at: occurredAt})});
    const record = {kind, document: review.document_id, intent, state: 'pending', receipt: null, busy: false, terminalResponse: false};
    const entry = Object.freeze({kind, document: record.document, intent, get state() {return record.state;}, get receipt() {return record.receipt;}});
    this.#records.set(entry, record);
    return entry;
  }
  #record(entry) {
    const record = this.#records.get(entry);
    if (!record) throw new Error('review_required');
    if (record.busy) throw new Error('operation_pending');
    return record;
  }
  #lock() {
    this.#locked = true;
    ++this.#generation;
    for (const record of this.#records.values()) record.state = 'locked';
  }
  restoreDetected() { this.#lock(); }
  authenticationChanged() { this.#lock(); }
  #sameContext(response) {
    if ([401, 403].includes(response.status) || response.body?.error?.code === 'instance_restored' || response.client !== this.#context.client || response.epoch !== this.#context.epoch) {
      this.#lock(); return false;
    }
    return true;
  }
  #receipt(record, receipt) {
    const terminal = ['source_drift', 'dependency_failed', 'invalid_dependency', 'forbidden', 'not_found'];
    if (receipt?.operation_id !== record.intent.operation_id || (receipt.outcome === 'accepted' ? !Number.isSafeInteger(receipt.version_id) || receipt.version_id <= 0 : !terminal.includes(receipt.outcome))) {
      this.#lock(); return false;
    }
    record.receipt = Object.freeze({...receipt});
    record.state = receipt.outcome === 'accepted' ? 'accepted' : 'needs_review';
    return true;
  }
  async #inspect(record, generation) {
    const response = await this.#request('GET', `/pdf-operations/${record.intent.operation_id}`);
    if (generation !== this.#generation) return false;
    if (response.status >= 500 || response.status === 429) {record.state = 'unknown'; return false;}
    if (!this.#sameContext(response)) return false;
    if (response.status === 200) return this.#receipt(record, response.body?.data);
    if (response.status !== 404 || response.body?.error?.code !== 'not_found' || record.receipt) {this.#lock(); return false;}
    if (record.terminalResponse) {record.state = 'needs_review'; return false;}
    // Ordinary, authoritative receipt absence is necessary but insufficient:
    // recheck original source and current Generate authority without rewriting.
    const current = await this.#request('GET', `/${record.kind}s/${record.document}/pdf/source`);
    if (generation !== this.#generation) return false;
    if (current.status >= 500 || current.status === 429) {record.state = 'unknown'; return false;}
    if (!this.#sameContext(current)) return false;
    if (current.status !== 200) {this.#lock(); return false;}
    const source = current.body?.data;
    record.state = source?.document_id === record.document && source.job_id === record.intent.job_id && source.source_token === record.intent.source_token ? 'retryable' : 'needs_review';
    return false;
  }
  async reconcile(entry) {
    const record = this.#record(entry);
    if (this.#locked || !['unknown', 'retryable'].includes(record.state)) throw new Error('review_required');
    record.busy = true;
    const generation = this.#generation;
    try { return await this.#inspect(record, generation); }
    catch { if (generation === this.#generation) record.state = 'unknown'; return false; }
    finally { record.busy = false; }
  }
  async submit(entry) {
    const record = this.#record(entry);
    if (this.#locked) throw new Error('review_required');
    if (record.state === 'unknown') { await this.reconcile(entry); return; }
    if (record.state !== 'pending') throw new Error('review_required');
    return this.#post(record);
  }
  async retryUnreceived(entry, {confirm = false} = {}) {
    const record = this.#record(entry);
    if (!confirm || this.#locked || record.state !== 'retryable' || record.receipt) throw new Error('review_required');
    // The caller explicitly reviewed unchanged retry. A receipt that appeared,
    // scope loss, source drift or epoch change still blocks any new POST.
    await this.reconcile(entry);
    if (record.state === 'retryable' && !this.#locked) return this.#post(record);
  }
  async #post(record) {
    record.busy = true;
    record.state = 'unknown';
    const generation = this.#generation;
    try {
      const response = await this.#request('POST', `/${record.kind}s/${record.document}/pdf/generate`, record.intent);
      if (generation !== this.#generation) return;
      if (response.status >= 500 || response.status === 429) return;
      if (!this.#sameContext(response)) return;
      if (response.status === 200) {this.#receipt(record, response.body?.data); return;}
      record.terminalResponse = true;
      await this.#inspect(record, generation);
    } catch { if (generation === this.#generation) record.state = 'unknown'; }
    finally { record.busy = false; }
  }
}
module.exports = { PDFReference };
