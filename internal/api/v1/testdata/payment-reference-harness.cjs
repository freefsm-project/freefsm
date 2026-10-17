'use strict';
const { randomUUID } = require('node:crypto');
const terminalOutcomes = new Set(['not_found', 'forbidden', 'invalid_payment', 'invoice_ineligible']);
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const inputFields = new Set(['amount', 'method', 'date', 'reference', 'note', 'other_description', 'occurred_at']);

function intentView(record) {
  const view = { operation_id: record.id };
  for (const name of ['job_id', ...inputFields]) {
    Object.defineProperty(view, name, {
      enumerable: true,
      get: () => record.sensitive?.readable ? record.sensitive.intent[name] : undefined,
    });
  }
  return Object.freeze(view);
}

// The view owns no permanent copy of sensitive input. Clearing/withholding the
// private record also removes access through every retained entry handle.
function entryView(record) {
  const projection = intentView(record);
  return Object.freeze({
    operation_id: record.id,
    get state() { return record.state; },
    get receipt() { return record.receipt; },
    get invoice() { return record.sensitive?.readable ? record.sensitive.invoice : undefined; },
    get intent() { return record.sensitive?.readable ? projection : undefined; },
  });
}

// One authenticated-context, in-memory reference; not a production encrypted cache.
class PaymentReference {
  #records = new Map();
  #pending = null;
  #restored = false;
  #revoked = false;
  constructor(request) { this.request = request; }
  get pending() { return this.#pending; }
  #record(entry) {
    const record = this.#records.get(entry);
    if (!record) throw new Error('Unknown original operation');
    return record;
  }
  #purge() {
    // Ordinary denial invalidates this reference's cached authority context.
    // Keep safe identities/receipts, but no sensitive input from older entries.
    for (const record of this.#records.values()) {
      record.sensitive = undefined;
      record.state = 'purged';
    }
    this.#revoked = true; this.#restored = false;
  }
  stage(invoice, job, values) {
    if (this.#revoked) throw new Error('Fresh authorized reference context required');
    if (this.#restored || this.#pending && !['accepted', 'rejected', 'discarded'].includes(this.#pending.state)) throw new Error('Reconcile the original financial intent first');
    if (!Number.isSafeInteger(invoice) || invoice < 1 || !Number.isSafeInteger(job) || job < 1) throw new Error('Original Invoice and Job required');
    if (!values || typeof values !== 'object' || Array.isArray(values)) throw new Error('Payment input must be an object');
    const captured = {};
    for (const [name, value] of Object.entries(values)) {
      if (value === undefined) continue;
      if (!inputFields.has(name) || typeof value !== 'string') throw new Error('Payment input fields must use the declared string contract');
      captured[name] = value;
    }
    const intent = Object.freeze({ ...captured, job_id: job, operation_id: randomUUID() });
    const record = { id: intent.operation_id, state: 'pending', receipt: null, sensitive: { invoice, intent, readable: true } };
    const entry = entryView(record);
    this.#records.set(entry, record); this.#pending = entry;
    return entry;
  }
  async submit(entry) {
    const record = this.#record(entry);
    if (entry !== this.#pending || record.state !== 'pending' || this.#restored || this.#revoked || !record.sensitive?.readable) throw new Error('Explicit original-intent review required');
    record.state = 'unknown';
    try {
      // Transport receives a wire snapshot, never the retained backing object.
      const response = await this.request('POST', `/invoices/${record.sensitive.invoice}/payments`, Object.freeze({ ...record.sensitive.intent }));
      if (response.status === 200) this.#acceptReceipt(entry, response.body?.data);
      else if (response.status === 401) this.#purge();
      else await this.inspect(entry);
    } catch { /* Preserve the original unknown identity; never manufacture failure. */ }
    return entry;
  }
  #acceptReceipt(entry, receipt) {
    const record = this.#record(entry);
    if (receipt?.operation_id !== record.id || typeof receipt.accepted_at !== 'string' || !Number.isFinite(Date.parse(receipt.accepted_at)) ||
      (receipt.outcome === 'accepted' ? typeof receipt.payment_id !== 'string' || !uuidPattern.test(receipt.payment_id) : !terminalOutcomes.has(receipt.outcome) || receipt.payment_id !== undefined)) {
      record.state = 'locked'; return;
    }
    record.receipt = Object.freeze({ operation_id: receipt.operation_id, outcome: receipt.outcome, accepted_at: receipt.accepted_at,
      ...(receipt.payment_id === undefined ? {} : { payment_id: receipt.payment_id }) });
    if (['forbidden', 'not_found'].includes(receipt.outcome)) { this.#purge(); return; }
    record.state = !record.sensitive ? 'purged' : receipt.outcome === 'accepted' ? 'accepted' : 'rejected';
  }
  async inspect(entry) {
    const record = this.#record(entry);
    if (record.sensitive) record.sensitive.readable = false;
    try {
      const response = await this.request('GET', `/payment-operations/${record.id}`);
      if (response.status === 401) {
        if (this.#restored) { if (record.sensitive) record.sensitive.readable = false; record.state = 'locked'; }
        else this.#purge();
        return entry;
      }
      if (response.status === 403) { this.#purge(); return entry; }
      if (response.status === 200) this.#acceptReceipt(entry, response.body?.data);
      else record.state = response.status === 404 && !record.receipt ? 'unknown' : 'locked';
      // Receipt ownership is not permission to retain Invoice/Payment input.
      if (record.sensitive) {
        const access = await this.request('GET', `/invoices/${record.sensitive.invoice}/payment-methods`);
        if (!record.sensitive) return entry; // A concurrent logout/denial wins.
        if (access.status === 200) record.sensitive.readable = true;
        else if ([401, 403, 404].includes(access.status)) this.#purge();
        else { record.sensitive.readable = false; record.state = 'locked'; }
      }
    } catch { record.state = 'locked'; }
    return entry;
  }
  restoreDetected() {
    this.#restored = true;
    for (const record of this.#records.values()) if (record.sensitive) record.sensitive.readable = false;
    if (this.#pending) this.#record(this.#pending).state = 'locked';
  }
  async reconcileAfterFreshAuthentication(entry) {
    if (!this.#restored) throw new Error('No restore review pending');
    await this.inspect(entry);
    const record = this.#record(entry);
    // Missing post-backup history never permits automatic financial replay.
    if (record.sensitive?.readable && record.receipt && ['accepted', 'rejected'].includes(record.state)) this.#restored = false;
    return entry;
  }
  confirmUnreceivedAfterReview(entry) {
    const record = this.#record(entry);
    if (entry !== this.#pending || record.state !== 'unknown' || record.receipt || !record.sensitive?.readable) throw new Error('Inspect original receipt and explicitly reconcile authorized history first');
    record.sensitive = undefined; record.state = 'discarded'; this.#restored = false;
  }
  logout() {
    this.#purge(); this.request = null;
  }
}
module.exports = { PaymentReference };
