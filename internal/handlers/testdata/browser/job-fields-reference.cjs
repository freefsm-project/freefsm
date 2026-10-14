const assert = require('node:assert/strict');
const { randomUUID } = require('node:crypto');
const { JobFieldReference } = require('../../../api/v1/reference/job-fields.cjs');
(async () => {
  const [base, bearer, job] = process.argv.slice(2);
  const client = new JobFieldReference(base, bearer);
  const initial = await client.read(job);
  const first = client.stage(job, initial, [{ definition_id: 1, value: 'A' }], randomUUID());
  const second = client.stage(job, initial, [{ definition_id: 2, value: 'B' }], randomUUID());
  assert.equal((await client.submit(first)).status, 200);
  assert.equal((await client.submit(second)).status, 200);
  assert.equal((await client.call(job, 'PATCH', first.command)).status, 200, 'same intent replay');
  const changedIntent = structuredClone(first.command); changedIntent.values[0].value = 'must reject';
  assert.equal((await client.call(job, 'PATCH', changedIntent)).body.error.code, 'operation_intent_changed');
  let current = await client.read(job);
  assert.deepEqual(current.values, [{ definition_id: 1, value: 'A' }, { definition_id: 2, value: 'B' }]);
  const stale = client.stage(job, initial, [{ definition_id: 1, value: 'stale' }], randomUUID());
  assert.equal((await client.submit(stale)).body.error.code, 'stale_field_revision');
  await assert.rejects(client.submit(stale), /explicit repair/);
  assert.throws(() => client.repair(stale, current, stale.command.values, stale.command.operation_id), /new reviewed ID/);
  const repaired = client.repair(stale, current, [{ definition_id: 1, value: 'Reviewed' }], randomUUID());
  assert.equal((await client.submit(repaired)).status, 200);
  current = await client.read(job);
  const invalid = client.stage(job, current, [{ definition_id: 1, value: 'partial must not persist' }, { definition_id: 2, value: 'invalid\ntext' }], randomUUID());
  assert.equal((await client.submit(invalid)).body.error.code, 'invalid_custom_field');
  assert.deepEqual((await client.read(job)).values, current.values, 'whole patch atomic');
  // Consume the actual successful HTTP response, then simulate its loss to the
  // caller. The reference must reconcile the original ID, not create new intent.
  let lose = true;
  const unreliable = new JobFieldReference(base, bearer, async (...args) => {
    const response = await fetch(...args);
    if (lose && args[1].method === 'PATCH') { lose = false; await response.arrayBuffer(); throw new Error('lost response'); }
    return response;
  });
  const uncertain = unreliable.stage(job, current, [{ definition_id: 2, value: 'Accepted once' }], randomUUID());
  await assert.rejects(unreliable.submit(uncertain), /lost response/);
  assert.equal(uncertain.disposition, 'unknown');
  assert.equal((await unreliable.reconcile(uncertain)).body.data.outcome, 'accepted');
  await assert.rejects(unreliable.submit(uncertain), /explicit repair/);
  const accepted = await client.read(job);
  assert.equal(accepted.values[1].value, 'Accepted once');
  const noop = client.stage(job, accepted, [{ definition_id: 2, value: 'Accepted once' }], randomUUID());
  assert.equal((await client.submit(noop)).status, 200);
  assert.deepEqual((await client.read(job)).revisions, accepted.revisions, 'no-op preserves revisions and attribution');
  console.log('Real HTTP reference: staged/disjoint patches, same-intent replay, changed-intent rejection, terminal conflict/new-ID repair, atomic validation and lost-response receipt reconciliation.');
})().catch(error => { console.error(error); process.exitCode = 1; });
