const assert = require('node:assert/strict');
const {test} = require('node:test');
const {randomUUID} = require('node:crypto');
const {JobExecutionReference} = require('../reference/job-execution.js');

test('uncertain Notes require original receipt reconciliation before explicit retry', async () => {
  const calls = [];
  let receipt;
  const call = async (path, method, intent) => {
    calls.push({path, method, intent});
    if (method === 'GET') return receipt || {status:404, error:{code:'not_found'}};
    throw new Error('connection lost');
  };
  const client = new JobExecutionReference(call, Date.now()+60000);
  const id = randomUUID();
  client.stage(42, 'field-notes', {notes_revision:randomUUID(), field_notes:'Original observation'}, id);
  await assert.rejects(client.submit(id), /connection lost/);
  await assert.rejects(client.submit(id), /reconciliation_required/);
  assert.equal(calls.filter(c=>c.method==='POST').length,1);
  await client.reconcile(id);
  assert.equal(calls.filter(c=>c.method==='POST').length,1,'receipt absence never automatically replays');
  await assert.rejects(client.submit(id), /connection lost/);
  assert.deepEqual(calls.filter(c=>c.method==='POST')[0],calls.filter(c=>c.method==='POST')[1]);
  receipt = {status:200,data:{operation_id:id,code:'accepted',effect_id:randomUUID(),accepted_at:new Date().toISOString()}};
  await client.reconcile(id);
  await assert.rejects(client.submit(id), /review_required/);
  assert.equal(client.pending.get(id).intent,undefined);
});

test('restore uncertainty locks pending intent and never turns a missing receipt into replay authority', async () => {
  let response = {status:401,error:{code:'instance_restored',recovery_disposition:'locked_review'}};
  let posts = 0;
  const client = new JobExecutionReference(async (_path, method) => { if(method==='POST')posts++; return response; }, Date.now()+60000);
  const id = randomUUID();
  client.stage(42,'field-notes',{notes_revision:randomUUID(),field_notes:'Recoverable pending intent'},id);
  await client.submit(id);
  assert.equal(client.pending.get(id).state,'locked_review');
  assert.equal(client.pending.get(id).intent.field_notes,'Recoverable pending intent');
  response = {status:404,error:{code:'not_found'}};
  await client.reconcile(id);
  await assert.rejects(client.submit(id), /review_required/);
  assert.equal(posts,1);
  client.discard(id);
  assert.equal(client.pending.has(id),false);
});

test('receipt identity mismatch never acknowledges or erases the original intent', async () => {
  const client = new JobExecutionReference(async()=>({status:200,data:{operation_id:randomUUID(),code:'accepted',accepted_at:new Date().toISOString()}}),Date.now()+60000);
  const id = randomUUID();
  client.stage(42,'field-notes',{notes_revision:randomUUID(),field_notes:'Original'},id);
  await assert.rejects(client.reconcile(id),/receipt_mismatch/);
  assert.equal(client.pending.get(id).intent.field_notes,'Original');
});
