(() => {
  'use strict';
  let bearer = '', deviceCode = '', epoch = '', redeemOperation = '', cancelOperation = '', busy = false;
  let transfers = [];
  const node = id => document.getElementById(id);
  const status = text => { node('status').textContent = text; };
  const update = () => {
    for (const id of ['redeem', 'cancel']) node(id).disabled = busy || !deviceCode;
    for (const id of ['inspect', 'logout', 'all', 'revoke']) node(id).disabled = busy || !bearer;
    node('start').querySelector('button').disabled = busy || !!deviceCode || !!bearer;
    node('transfer-start').disabled = busy || !bearer;
    node('transfer-retry').disabled = busy || !bearer || !transfers.some(t => !t.verified);
  };
  const clear = () => { bearer = ''; deviceCode = ''; epoch = ''; redeemOperation = ''; cancelOperation = ''; transfers = []; node('transfer-files').value = ''; node('transfer-results').textContent = ''; node('code').textContent = ''; node('sessions').textContent = ''; update(); };
  async function call(path, method, body, authenticated = false, raw = false) {
    const headers = {};
    if (body) headers['Content-Type'] = raw ? 'application/octet-stream' : 'application/json';
    if (authenticated) {
      headers.Authorization = `Bearer ${bearer}`;
      if (epoch) headers['X-FreeFSM-Session-Epoch'] = epoch;
    }
    const response = await fetch(`/api/v1${path}`, { method, headers, credentials: 'omit', cache: 'no-store', redirect: 'error', body: body ? (raw ? body : JSON.stringify(body)) : undefined });
    const result = response.status === 204 ? null : await response.json();
    if (!response.ok) {
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
    try { await fn(); } catch { status('Response unavailable. Inspect browser sessions before explicitly retrying; no action was replayed.'); }
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
      bearer = data.token; deviceCode = ''; node('code').textContent = 'Paired. Bearer held privately in memory.';
    }
  }));
  node('cancel').addEventListener('click', action(async () => { if (await call('/pairings/cancel', 'POST', { device_code: deviceCode, operation_id: cancelOperation })) clear(); }));
  node('inspect').addEventListener('click', action(async () => {
    const data = await call('/sessions', 'GET', null, true);
    if (Array.isArray(data)) node('sessions').textContent = data.map(s => `${s.id}: ${s.kind} ${s.device} ${s.current ? '(current)' : ''} ${s.revoked_at ? '(revoked)' : ''}`).join('\n');
  }));
  node('logout').addEventListener('click', action(async () => { if (await call('/session', 'DELETE', null, true)) clear(); }));
  node('all').addEventListener('click', action(async () => { if (await call('/sessions', 'DELETE', null, true)) clear(); }));
  node('revoke').addEventListener('click', action(async () => {
    const id = node('session').value;
    if (!/^[1-9][0-9]*$/.test(id)) { status('Enter an own session ID from inspection.'); return; }
    await call(`/sessions/${id}`, 'DELETE', null, true);
  }));
  const transferResults = () => { node('transfer-results').textContent = transfers.map(t => `${t.file.name}: ${t.state}`).join('\n'); };
  async function runTransfers() {
    const limits = await call('/files/limits', 'GET', null, true);
    if (!limits) return;
    for (const item of transfers) {
      if (!bearer) break;
      if (item.verified) continue;
      try {
        if (!item.file.size || item.file.size > limits.max_file_bytes) throw new Error('file_limit');
        if (!item.intent) {
          const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', await item.file.arrayBuffer()));
          item.intent = {id: item.id, publication_id: item.publication, object_type: 'job', object_id: item.parent,
            name: item.file.name, size: item.file.size, sha256: Array.from(digest, b => b.toString(16).padStart(2, '0')).join('')};
        }
        let state = await call('/file-transfers', 'POST', item.intent, true);
        if (!state) throw new Error('start_failed');
        if (state.size !== item.file.size || state.sha256 !== item.intent.sha256 || state.object_id !== item.parent) throw new Error('resume_mismatch');
        while (state.transferred < item.file.size) {
          const offset = state.transferred;
          item.state = `${offset}/${item.file.size} transport bytes; unverified`; transferResults();
          const bytes = await item.file.slice(offset, offset + limits.max_chunk_bytes).arrayBuffer();
          state = await call(`/file-transfers/${item.id}/chunks?offset=${offset}`, 'PUT', bytes, true, true);
          if (!state || state.transferred !== offset + bytes.byteLength) throw new Error('chunk_failed');
        }
        item.state = 'transport complete; verifying'; transferResults();
        const verified = await call(`/file-transfers/${item.id}/verify`, 'POST', null, true);
        if (!verified || verified.verified !== true || verified.sha256 !== item.intent.sha256) throw new Error('verification_failed');
        item.verified = true; item.state = 'verified; not published';
      } catch {
        item.state = 'failed; retry unchanged bytes and original parent';
      }
      transferResults();
    }
  }
  node('transfer-start').addEventListener('click', action(async () => {
    const files = Array.from(node('transfer-files').files);
    if (!files.length || files.length > 10) { status('Select at most 10 Files per batch.'); return; }
    const parent = Number(node('transfer-job').value);
    if (!Number.isSafeInteger(parent) || parent <= 0) { status('Enter a valid original Job ID.'); return; }
    transfers = files.map(file => ({file, parent, id: crypto.randomUUID(), publication: crypto.randomUUID(), state: 'queued', verified: false}));
    await runTransfers();
  }));
  node('transfer-retry').addEventListener('click', action(runTransfers));
  window.addEventListener('pagehide', clear);
  update();
})();
