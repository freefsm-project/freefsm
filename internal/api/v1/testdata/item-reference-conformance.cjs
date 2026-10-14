const assert = require('node:assert/strict');
const { ItemReferenceHarness } = require('./item-reference-harness.cjs');

(async () => {
  const id = Number(process.env.ITEM_ID);
  const lookup = () => fetch(`${process.env.ITEM_API_URL}/items/${id}/selection`, {
    headers: { Authorization: `Bearer ${process.env.ITEM_API_TOKEN}` },
  });
  const harness = new ItemReferenceHarness(lookup);
  const reference = await harness.refresh(id);
  assert.equal(reference.unit_price, '23.45');
  assert.equal('unit_cost' in reference, false);
  const capture = harness.stageSelection(id, 1.5);
  assert.equal('unit_cost' in capture, false);
  capture.unit_price = 23.45;
  assert.deepEqual(await harness.reviewSelection(capture), capture);
  harness.logout();
  assert.throws(() => harness.stageSelection(id), /selection_unavailable/);
  for (const method of ['POST', 'PATCH', 'DELETE']) {
    const response = await fetch(`${process.env.ITEM_API_URL}/items/${id}`, {
      method, headers: { Authorization: `Bearer ${process.env.ITEM_API_TOKEN}` },
    });
    assert.equal(response.status, 405, `maintenance ${method} must stay outside the field API`);
  }
  await harness.refresh(id);
  // One explicitly reviewed logout intent; unchanged retries retain this UUID.
  const logoutOperation = require('node:crypto').randomUUID();
  const revoked = await fetch(`${process.env.ITEM_API_URL}/session`, {
    method: 'DELETE', headers: { Authorization: `Bearer ${process.env.ITEM_API_TOKEN}`, 'Operation-ID': logoutOperation },
  });
  assert.equal(revoked.status, 204);
  await assert.rejects(() => harness.refresh(id), /selection_requires_review/);
  assert.throws(() => harness.stageSelection(id), /selection_unavailable/);
  process.stdout.write(JSON.stringify(capture));
})().catch(error => { console.error(error); process.exitCode = 1; });
