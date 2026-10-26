'use strict';
const assert = require('node:assert/strict');
const { randomUUID, createHash } = require('node:crypto');
const { PDFReference } = require('../reference/pdf-client.cjs');
const [base, token, document] = process.argv.slice(2);
(async () => {
  const request = async (method, path, input, binding) => {
    if (binding && binding.client !== createHash('sha256').update(token).digest('hex')) throw new Error('authentication_changed');
    const response = await fetch(base + path, { method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json', ...(binding ? {'X-FreeFSM-Session-Epoch': binding.epoch} : {}) }, body: input ? JSON.stringify(input) : undefined });
    return { status: response.status, body: await response.json(), client: createHash('sha256').update(token).digest('hex'), epoch: response.headers.get('X-FreeFSM-Session-Epoch') };
  };
  const path = `/invoices/${document}`;
  const reviewed = await request('GET', path + '/pdf/source');
  const review = reviewed.body.data;
  let lose = true;
  let posts = 0;
  const client = new PDFReference(async (method, path, body, binding) => {
    if (method === 'POST') posts++;
    const result = await request(method, path, body, binding);
    if (method === 'POST' && lose) { lose = false; throw new Error('lost response after commit'); }
    return result;
  }, {client: reviewed.client, epoch: reviewed.epoch});
  const entry = client.stage('invoice', review);
  assert.equal(posts, 0, 'offline staging contacted server');
  await client.submit(entry); assert.equal(entry.state, 'unknown');
  await client.submit(entry); assert.equal(entry.state, 'accepted'); assert.equal(posts, 1);
  assert.ok(entry.receipt.version_id);
  const source = (await request('GET', path)).body.data;
  assert.equal((await request('PATCH', path, { operation_id: randomUUID(), job_id: source.job_id, revision: source.revision, notes: 'Reference changed rendered notes' })).status, 200);
  const stale = client.stage('invoice', review);
  await client.submit(stale); assert.equal(stale.state, 'needs_review'); assert.equal(stale.receipt.outcome, 'source_drift');
  await assert.rejects(client.submit(stale), /review_required/);
  const current = (await request('GET', path + '/pdf/source')).body.data;
  const child = client.stage('invoice', current, [stale.intent.operation_id]);
  await client.submit(child); assert.equal(child.state, 'needs_review'); assert.equal(child.receipt.outcome, 'dependency_failed');
  const repaired = client.stage('invoice', current);
  await client.submit(repaired); assert.equal(repaired.state, 'accepted');
  assert.notEqual(repaired.receipt.version_id, entry.receipt.version_id);
  console.log('PASS PDF reference: offline intent only, lost-response receipt, source drift, blocked dependent, explicit new review');
})().catch(error => { console.error(error); process.exit(1); });
