(() => {
  'use strict';
  let bearer = '', deviceCode = '', epoch = '', redeemOperation = '', cancelOperation = '', busy = false;
  let transfers = [];
  let authenticationGeneration = 0, authenticatedUser = null;
  const sessionOperations = new Map();
  const node = id => document.getElementById(id);
  const status = text => { node('status').textContent = text; };
  const update = () => {
    for (const id of ['redeem', 'cancel']) node(id).disabled = busy || !deviceCode;
    for (const id of ['inspect', 'logout', 'all', 'revoke']) node(id).disabled = busy || !bearer;
    node('start').querySelector('button').disabled = busy || !!deviceCode || !!bearer;
    node('transfer-start').disabled = busy || !bearer;
    node('transfer-retry').disabled = busy || !bearer || !transfers.some(t => !t.verified && !t.review);
  };
  const clear = () => { bearer = ''; authenticatedUser = null; deviceCode = ''; epoch = ''; redeemOperation = ''; cancelOperation = ''; transfers = []; node('transfer-files').value = ''; node('transfer-results').textContent = ''; node('code').textContent = ''; node('sessions').textContent = ''; update(); };
  async function call(path, method, body, authenticated = false, raw = false, options = {}) {
    const headers = {};
    if (options.operation) headers['Operation-ID'] = options.operation;
    if (body) headers['Content-Type'] = raw ? 'application/octet-stream' : 'application/json';
    if (authenticated) {
      headers.Authorization = `Bearer ${bearer}`;
      if (epoch) headers['X-FreeFSM-Session-Epoch'] = epoch;
    }
    const response = await fetch(`/api/v1${path}`, { method, headers, credentials: 'omit', cache: 'no-store', redirect: 'error', body: body ? (raw ? body : JSON.stringify(body)) : undefined });
    const result = response.status === 204 ? null : await response.json();
    if (!response.ok) {
      if (options.receiptLookup && response.status === 404 && result?.error?.code === 'not_found') return {unreceived: true};
      if (result?.error?.recovery_disposition === 'purge' || result?.error?.recovery_disposition === 'locked_review') clear();
      status(`HTTP ${response.status}: ${result?.error?.code || 'unknown_error'}. ${result?.error?.recovery_disposition || 'review'}; no automatic retry.`);
      return null;
    }
    if (authenticated) epoch = response.headers.get('X-FreeFSM-Session-Epoch') || epoch;
    status(`HTTP ${response.status}: completed`);
    return result?.data || {};
  }
  const action = fn => async event => {
    event.preventDefault(); if (busy) return; busy = true; update();
    try { await fn(); } catch { status(`Response unavailable. Inspect browser sessions before explicitly retrying; no action was replayed.${sessionOperations.size ? ' Pending session operation IDs: '+Array.from(sessionOperations.values(),op=>op.id).join(', ') : ''}`); }
    finally { busy = false; update(); }
  };
  node('start').addEventListener('submit', action(async () => {
    const data = await call('/pairings', 'POST', { device_name: node('device').value, operation_id: crypto.randomUUID() });
    if (data) { deviceCode = data.device_code; redeemOperation = crypto.randomUUID(); cancelOperation = crypto.randomUUID(); node('code').textContent = `Enter code ${data.user_code} in browser approval. Expires ${data.expires_at}.`; }
  }));
  node('redeem').addEventListener('click', action(async () => {
    const data = await call('/pairings/redeem', 'POST', { device_code: deviceCode, operation_id: redeemOperation });
    if (data) {
      if (data.token_type !== 'Bearer' || typeof data.token !== 'string') { clear(); status('Unsupported protocol response. Inspect and revoke the device in browser sessions.'); return; }
      bearer = data.token; authenticationGeneration++; deviceCode = '';
      const identity = await call('/me', 'GET', null, true);
      if (!identity || !Number.isSafeInteger(identity.id)) { clear(); status('Fresh identity unavailable. Pair again before reviewing pending actions.'); return; }
      authenticatedUser = identity.id;
      node('code').textContent = 'Paired. Bearer held privately in memory.';
    }
  }));
  node('cancel').addEventListener('click', action(async () => { if (await call('/pairings/cancel', 'POST', { device_code: deviceCode, operation_id: cancelOperation })) clear(); }));
  node('inspect').addEventListener('click', action(async () => {
    const data = await call('/sessions', 'GET', null, true);
    if (Array.isArray(data)) node('sessions').textContent = data.map(s => `${s.id}: ${s.kind} ${s.device} ${s.current ? '(current)' : ''} ${s.revoked_at ? '(revoked)' : ''}`).join('\n');
  }));
  async function revoke(path) {
    let operation = sessionOperations.get(path);
    if (operation) {
      const receipt = await call(`/session-operations/${operation.id}`, 'GET', null, true, false, {receiptLookup:true});
      if (!receipt) return null;
      if (!receipt.unreceived) {
        status(`Session operation ${operation.id}: ${receipt.code}. No action replayed.`);
        sessionOperations.delete(path);
        return receipt.code === 'accepted' ? {...receipt,reconciled:true} : null;
      }
      if (operation.generation !== authenticationGeneration || operation.user !== authenticatedUser) {
        status(`Session operation ${operation.id}: no receipt found. Fresh-authentication reconciliation is read-only; outcome remains unresolved. Reload and review a new security action with a new operation ID if still needed.`);
        return null;
      }
    } else {
      if (authenticatedUser === null) { status('Fresh identity required before a security action.'); return null; }
      operation = {id:crypto.randomUUID(),generation:authenticationGeneration,user:authenticatedUser}; sessionOperations.set(path, operation);
    }
    status(`Session operation ${operation.id}: awaiting response; retain this ID for reconciliation.`);
    const result = await call(path, 'DELETE', null, true, false, {operation:operation.id});
    if (result) sessionOperations.delete(path);
    return result;
  }
  node('logout').addEventListener('click', action(async () => { const result=await revoke('/session'); if (result && !result.reconciled) clear(); }));
  node('all').addEventListener('click', action(async () => { const result=await revoke('/sessions'); if (result && !result.reconciled) clear(); }));
  node('revoke').addEventListener('click', action(async () => {
    const id = node('session').value;
    if (!/^[1-9][0-9]*$/.test(id)) { status('Enter an own session ID from inspection.'); return; }
    await revoke(`/sessions/${id}`);
  }));
  const transferResults = () => { node('transfer-results').textContent = transfers.map(t => `${t.file.name}: ${t.state}`).join('\n'); };
  async function transport(item, operation, path, body, raw = false) {
    if (operation.attempted) {
      const receipt = await call(`/file-transfer-operations/${operation.id}`, 'GET', null, true, false, {receiptLookup: true});
      if (!receipt) throw new Error('reconciliation_unavailable');
      if (!receipt.unreceived) {
        if (receipt.operation_id !== operation.id || !receipt.accepted_at) throw new Error('receipt_mismatch');
        if (receipt.code !== 'accepted') {
          item.review = true;
          throw new Error('terminal_receipt');
        }
        if (receipt.transfer_id !== item.id) throw new Error('receipt_mismatch');
        // A safe receipt is not authority to read the transfer representation.
        return call(`/file-transfers/${item.id}`, 'GET', null, true);
      }
      // Only this explicit retry, after a successful receipt miss, may replay.
    }
    operation.attempted = true;
    return call(path, raw ? 'PUT' : 'POST', body, true, raw, {operation: operation.id});
  }
  async function runTransfers() {
    const limits = await call('/files/limits', 'GET', null, true);
    if (!limits) return;
    for (const item of transfers) {
      if (!bearer) break;
      if (item.verified || item.review) continue;
      try {
        if (!item.file.size || item.file.size > limits.max_file_bytes) throw new Error('file_limit');
        if (!item.intent) {
          const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', await item.file.arrayBuffer()));
          item.intent = {id: item.id, publication_id: item.publication, object_type: 'job', object_id: item.parent,
            name: item.file.name, size: item.file.size, sha256: Array.from(digest, b => b.toString(16).padStart(2, '0')).join('')};
        }
        let state = await transport(item, item.start, '/file-transfers', item.intent);
        if (!state) throw new Error('start_failed');
        if (state.size !== item.file.size || state.sha256 !== item.intent.sha256 || state.object_id !== item.parent) throw new Error('resume_mismatch');
        while (item.pendingChunk || state.transferred < item.file.size) {
          if (!item.pendingChunk) {
            const offset = state.transferred;
            const bytes = await item.file.slice(offset, offset + limits.max_chunk_bytes).arrayBuffer();
            item.pendingChunk = {id: crypto.randomUUID(), offset, bytes};
          }
          const chunk = item.pendingChunk;
          const {offset, bytes} = chunk;
          item.state = `${offset}/${item.file.size} transport bytes; unverified`; transferResults();
          state = await transport(item, chunk, `/file-transfers/${item.id}/chunks?offset=${offset}`, bytes, true);
          if (!state || state.transferred !== offset + bytes.byteLength) throw new Error('chunk_failed');
          item.pendingChunk = null;
        }
        item.state = 'transport complete; verifying'; transferResults();
        const verified = await call(`/file-transfers/${item.id}/verify`, 'POST', null, true);
        if (!verified || verified.verified !== true || verified.sha256 !== item.intent.sha256) throw new Error('verification_failed');
        item.verified = true; item.state = 'verified; not published';
      } catch {
        item.state = item.review ? 'failed; terminal receipt requires reviewed new intent and new operation UUID' : 'failed; retry reconciles original operations before sending unchanged bytes';
      }
      transferResults();
    }
  }
  node('transfer-start').addEventListener('click', action(async () => {
    const files = Array.from(node('transfer-files').files);
    if (!files.length || files.length > 10) { status('Select at most 10 Files per batch.'); return; }
    const parent = Number(node('transfer-job').value);
    if (!Number.isSafeInteger(parent) || parent <= 0) { status('Enter a valid original Job ID.'); return; }
    transfers = files.map(file => ({file, parent, id: crypto.randomUUID(), publication: crypto.randomUUID(), start: {id: crypto.randomUUID()}, state: 'queued', verified: false}));
    await runTransfers();
  }));
  node('transfer-retry').addEventListener('click', action(runTransfers));
  window.addEventListener('pagehide', clear);
  update();
})();
