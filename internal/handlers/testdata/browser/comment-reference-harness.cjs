// Deliberately small reference harness for OFF-05. The pending file belongs to
// this disposable test fixture, not a product cache or an offline lease store.
const fs = require('node:fs');
const assert = require('node:assert/strict');
const { randomUUID } = require('node:crypto');
(async () => {
  const [base, token, job, pendingFile] = process.argv.slice(2);
  const path = `/jobs/${job}/comments`;
  const call = async (method, path, body, status) => {
    const response = await fetch(base + path, { method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
    const json = await response.json();
    assert.equal(response.status, status, JSON.stringify(json));
    return json.data;
  };
  const parentRevision=(await call('GET',path,null,200)).eligibility_revision;assert(parentRevision);
  const pending = { path, body: { operation_id: randomUUID(), content: 'Reference harness pending Comment', parent_revision:parentRevision } };
  fs.writeFileSync(pendingFile, JSON.stringify(pending), { mode: 0o600 });
  const restored = JSON.parse(fs.readFileSync(pendingFile));
  // Lose the response, then restart reconciliation from persisted exact intent.
  await call('POST', restored.path, restored.body, 200);
  const receipt = await call('GET', `/comment-operations/${restored.body.operation_id}`, null, 200);
  assert.equal(receipt.code, 'accepted');
  assert.deepEqual(await call('POST', restored.path, restored.body, 200), receipt);
  const reply = { operation_id: randomUUID(), content: 'Pending reply to exact original', reply_to: receipt.comment_id, parent_revision:parentRevision };
  const removePath = `${path}/${receipt.comment_id}/remove`;
  await call('POST', removePath, { operation_id: randomUUID(), parent_revision:parentRevision }, 200);
  await call('POST', removePath, { operation_id: randomUUID(), parent_revision:parentRevision }, 200);
  await call('POST', path, reply, 409);
  await call('POST', path, reply, 409);
  const rejected = await call('GET', `/comment-operations/${reply.operation_id}`, null, 200);
  assert.equal(rejected.code, 'comment_conflict');
  const list = await call('GET', path, null, 200);
  assert.equal(list.comments.length, 1);
  assert.equal(list.comments[0].content, '');
  assert.ok(list.comments[0].removed_at);
  console.log('Persisted pending intent: lost-response reconciliation, one effect, own-removal no-op, terminal closed-thread rejection without retargeting.');
})().catch(error => { console.error(error); process.exitCode = 1; });
