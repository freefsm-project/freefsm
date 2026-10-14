'use strict';

// Executable reference for the field-specific protocol. Intent is held only in
// memory. The owning sync client supplies secure storage/leases; this harness
// never treats staging or local occurrence as server acceptance.
class JobFieldReference {
  constructor(base, bearer, transport = fetch) {
    this.base = base; this.bearer = bearer; this.transport = transport;
  }
  async call(job, method, body, operation) {
    const response = await this.transport(`${this.base}/jobs/${job}/custom-fields${operation ? `?operation_id=${operation}` : ''}`, {
      method, headers: { Authorization: `Bearer ${this.bearer}`, 'Content-Type': 'application/json' },
      body: body ? JSON.stringify(body) : undefined, redirect: 'error', cache: 'no-store',
    });
    return { status: response.status, body: await response.json() };
  }
  async read(job) {
    const response = await this.call(job, 'GET');
    if (response.status !== 200) throw new Error(`Field read rejected: ${response.status}`);
    return response.body.data;
  }
  stage(job, state, values, operationID) {
    const revisions = Object.fromEntries(values.map(v => [String(v.definition_id), state.revisions[String(v.definition_id)]]));
    return { job, command: structuredClone({ operation_id: operationID, revisions, values }), disposition: 'pending' };
  }
  async submit(pending) {
    if (!['pending', 'unknown', 'retry'].includes(pending.disposition)) throw new Error('Terminal operation needs explicit repair with a new ID');
    try {
      const response = await this.call(pending.job, 'PATCH', pending.command);
      if (response.status === 200) pending.disposition = 'accepted';
      else if (response.status === 429 || response.status >= 500) pending.disposition = 'retry';
      else pending.disposition = 'rejected';
      pending.result = response;
      if ([401, 403, 404].includes(response.status)) delete pending.comparison;
      else pending.comparison = response.body.comparison;
      return response;
    } catch (error) { pending.disposition = 'unknown'; throw error; }
  }
  async reconcile(pending) {
    const response = await this.call(pending.job, 'GET', null, pending.command.operation_id);
    if (response.status === 200) {
      pending.disposition = response.body.data.outcome === 'accepted' ? 'accepted' : 'rejected';
      pending.receipt = response.body.data;
      delete pending.comparison;
    }
    return response;
  }
  repair(pending, current, reviewedValues, newID) {
    if (pending.disposition !== 'rejected' || newID === pending.command.operation_id) throw new Error('Repair requires a terminal rejection and a new reviewed ID');
    return this.stage(pending.job, current, reviewedValues, newID);
  }
}
module.exports = { JobFieldReference };
