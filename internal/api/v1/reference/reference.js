(() => {
  'use strict';
  let bearer = '', deviceCode = '', epoch = '', redeemOperation = '', cancelOperation = '', busy = false;
  const node = id => document.getElementById(id);
  const status = text => { node('status').textContent = text; };
  const update = () => {
    for (const id of ['redeem', 'cancel']) node(id).disabled = busy || !deviceCode;
    for (const id of ['inspect', 'logout', 'all', 'revoke']) node(id).disabled = busy || !bearer;
    node('start').querySelector('button').disabled = busy || !!deviceCode || !!bearer;
  };
  const clear = () => { bearer = ''; deviceCode = ''; epoch = ''; redeemOperation = ''; cancelOperation = ''; node('code').textContent = ''; node('sessions').textContent = ''; update(); };
  async function call(path, method, body, authenticated = false) {
    const headers = {};
    if (body) headers['Content-Type'] = 'application/json';
    if (authenticated) {
      headers.Authorization = `Bearer ${bearer}`;
      if (epoch) headers['X-FreeFSM-Session-Epoch'] = epoch;
    }
    const response = await fetch(`/api/v1${path}`, { method, headers, credentials: 'omit', cache: 'no-store', redirect: 'error', body: body ? JSON.stringify(body) : undefined });
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
  window.addEventListener('pagehide', clear);
  update();
})();
