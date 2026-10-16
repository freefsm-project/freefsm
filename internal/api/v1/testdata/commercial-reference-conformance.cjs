const assert = require('node:assert/strict');
const { CommercialReference } = require('./commercial-reference-harness.cjs');
(async () => {
  const [base, token, job] = process.argv.slice(2);
  let calls = 0, lose = false;
  const request = async (method, path, body) => {
    calls++;
    const response = await fetch(base + path, { method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
    const result = { status: response.status, body: await response.json() };
    if (lose && method === 'POST') { lose = false; throw new Error('simulated lost response after real commit'); }
    return result;
  };
  const client = new CommercialReference(request);
  assert.throws(() => client.stageCreate('invoice', 0, { title: 'No fallback' }));
  for (const kind of ['estimate', 'invoice']) {
    const before = calls;
    const entry = client.stageCreate(kind, Number(job), { title: `Offline ${kind}`, line_items: [{ title: 'Captured work', quantity: 2, unit_price: '17.25', taxable: false }] });
    assert.equal(calls, before, 'offline staging contacted server');
    lose = true;
    await client.submit(entry);
    assert.equal(entry.state, 'unknown');
    await client.inspect(entry);
    assert.equal(entry.state, 'accepted');
    await assert.rejects(() => client.submit(entry));
    const path = `/${kind}s/${entry.receipt.id}`;
    const current = (await request('GET', path)).body.data;
    assert.equal(current.line_items[0].unit_price, '17.25');
    const a = client.stageEdit(kind, current, { notes: 'First field edit' });
    const b = client.stageEdit(kind, current, { terms: 'Disjoint stale edit' });
    await client.submit(a); assert.equal(a.state, 'accepted');
    await client.submit(b); assert.equal(b.state, 'needs_review');
    await assert.rejects(() => client.submit(b));
    const reviewed = (await request('GET', path)).body.data;
    assert.throws(() => client.reviewedEdit(b, { ...reviewed, job_id: Number(job) + 1 }, { terms: 'Retarget' }));
    const repair = client.reviewedEdit(b, reviewed, { terms: 'Explicitly reviewed terms' });
    assert.notEqual(repair.intent.operation_id, b.intent.operation_id);
    await client.submit(repair); assert.equal(repair.state, 'accepted');
    const persisted = (await request('GET', path)).body.data;
    assert.equal(persisted.notes, 'First field edit');
    assert.equal(persisted.terms, 'Explicitly reviewed terms');

    const category = kind === 'invoice' ? 'invoice:invoiced' : 'estimate:accepted';
    assert.ok(Array.isArray(persisted.workflow), 'review exposes current workflow meaning');
    const choice = persisted.workflow.find(x => x.category === category);
    assert.ok(choice, 'manual acceptance / finalize target discoverable');
    const stagedCalls = calls;
    const transition = client.stageLifecycle(kind, persisted, choice, kind === 'invoice' ? 'finalize' : 'status');
    assert.equal(calls, stagedCalls, 'staged lifecycle contacted server');
    lose = true;
    await client.submit(transition);
    assert.equal(transition.state, 'unknown');
    await client.inspect(transition);
    assert.equal(transition.state, 'accepted');
    await assert.rejects(() => client.submit(transition));
    const transitioned = (await request('GET', path)).body.data;
    assert.equal(transitioned.manual_category, category);
    const staleTransition = client.stageLifecycle(kind, persisted, choice, kind === 'invoice' ? 'finalize' : 'status');
    await client.submit(staleTransition);
    assert.equal(staleTransition.state, 'needs_review');
    assert.equal((await request('GET', path)).body.data.revision, transitioned.revision, 'stale lifecycle changed document');
  }
  console.log('PASS persisted commercial reference: offline stage, lost response receipt, create-once, disjoint conflict, explicit same-target repair');
})().catch(error => { console.error(error); process.exit(1); });
