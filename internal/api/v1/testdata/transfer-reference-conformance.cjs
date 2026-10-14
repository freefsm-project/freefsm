const assert = require('node:assert/strict');
const {readFileSync} = require('node:fs');
const {webcrypto} = require('node:crypto');
const vm = require('node:vm');
const {test} = require('node:test');

test('queued uploader reconciles lost responses and retains exact operation intent', async () => {
  const elements = new Map();
  const node = id => {
    if (!elements.has(id)) elements.set(id, {value: '', textContent: '', files: [], handlers: {},
      addEventListener(event, handler) { this.handlers[event] = handler; }, querySelector() { return {}; }});
    return elements.get(id);
  };
  const requests = [], receipts = new Map();
  let transfer, unavailable = true, chunkAttempts = 0;
  const response = (status, data) => ({status, ok: status < 400, headers: new Headers(),
    json: async () => status < 400 ? {data} : {error: {code: data}}});
  const fetch = async (url, request) => {
    const path = url.replace('/api/v1', '');
    requests.push({path, ...request});
    if (path === '/pairings') return response(200, {device_code: 'device', user_code: 'code'});
    if (path === '/pairings/redeem') return response(200, {token_type: 'Bearer', token: 'private'});
    if (path === '/files/limits') return response(200, {max_file_bytes: 100, max_chunk_bytes: 3});
    if (path.startsWith('/file-transfer-operations/')) {
      if (unavailable) return response(503, 'unavailable');
      const receipt = receipts.get(path.split('/').pop());
      return receipt ? response(200, receipt) : response(404, 'not_found');
    }
    const operation = request.headers['Operation-ID'];
    const accept = () => receipts.set(operation, {operation_id: operation, transfer_id: transfer.id,
      code: 'accepted', accepted_at: '2026-10-14T00:00:00Z'});
    if (path === '/file-transfers') {
      transfer = {...JSON.parse(request.body), transferred: 0}; accept();
      throw new Error('lost committed start response');
    }
    if (path.includes('/chunks?')) {
      chunkAttempts++;
      if (chunkAttempts === 1) throw new Error('request did not arrive');
      assert.equal(Number(new URL('https://example.test' + path).searchParams.get('offset')), transfer.transferred);
      transfer.transferred += request.body.byteLength; accept();
      if (chunkAttempts === 2) throw new Error('lost committed chunk response');
      return response(200, {...transfer});
    }
    if (path.endsWith('/verify')) return response(200, {...transfer, verified: true});
    if (path === `/file-transfers/${transfer.id}`) return response(200, {...transfer});
    throw new Error(`Unexpected request ${path}`);
  };
  vm.runInNewContext(readFileSync(require.resolve('../reference/reference.js'), 'utf8'), {
    document: {getElementById: node}, window: {addEventListener() {}}, crypto: webcrypto, fetch,
  });
  const click = (id, event = 'click') => node(id).handlers[event]({preventDefault() {}});
  await click('start', 'submit'); await click('redeem');
  node('transfer-job').value = '42';
  const file = new Blob(['abcdef']); file.name = 'sample.txt';
  node('transfer-files').files = [file];
  await click('transfer-start');
  assert.match(node('transfer-results').textContent, /failed/);
  await click('transfer-retry');
  assert.equal(requests.filter(r => r.path === '/file-transfers').length, 1, 'unavailable reconciliation never replays start');
  assert.equal(chunkAttempts, 0);
  unavailable = false;
  await click('transfer-retry'); // Accepted start, then unreceived chunk.
  assert.equal(chunkAttempts, 1);
  await click('transfer-retry'); // Receipt miss permits explicit identical replay.
  assert.equal(chunkAttempts, 2);
  await click('transfer-retry'); // Accepted chunk is not resent; next segment completes.
  assert.match(node('transfer-results').textContent, /verified; not published/);
  const writes = requests.filter(r => r.method === 'PUT');
  assert.equal(writes.length, 3);
  assert.equal(writes[0].headers['Operation-ID'], writes[1].headers['Operation-ID']);
  assert.equal(writes[0].path, writes[1].path);
  assert.deepEqual(Buffer.from(writes[0].body), Buffer.from(writes[1].body));
  assert.notEqual(writes[1].headers['Operation-ID'], writes[2].headers['Operation-ID']);
  const start = requests.find(r => r.path === '/file-transfers');
  const operations = [start.headers['Operation-ID'], writes[0].headers['Operation-ID'], writes[2].headers['Operation-ID']];
  assert.equal(new Set(operations).size, 3);
  for (const id of operations) {
    assert.match(id, /^[0-9a-f-]{36}$/);
    assert.notEqual(id, transfer.id); assert.notEqual(id, transfer.publication_id);
  }
  const count = requests.length;
  await click('transfer-retry');
  assert.equal(requests.length, count + 1, 'successful queue item only permits limits discovery, no resend');
});
