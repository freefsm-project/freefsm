(() => {
  'use strict';
  let bearer = '', deviceCode = '', epoch = '', redeemOperation = '', cancelOperation = '', busy = false, credentialGeneration = 0;
  let jobs = new Map(), staged = null, completion = '', nextRequest = '', lastRequest = '', syncScope = 'assigned', syncSize = 50, syncComplete = false, completionVersion = 0;
  let transfers = [];
  let authenticationGeneration = 0, authenticatedUser = null;
  const sessionOperations = new Map();
  const node = id => document.getElementById(id);
  const status = text => { node('status').textContent = text; };
  const update = () => {
    for (const id of ['redeem', 'cancel']) node(id).disabled = busy || !deviceCode;
    for (const id of ['inspect', 'logout', 'all', 'revoke']) node(id).disabled = busy || !bearer;
    node('start').querySelector('button').disabled = busy || !!deviceCode || !!bearer;
    node('sync-start').disabled = busy || !bearer;
    node('sync-next').disabled = busy || !bearer || !nextRequest;
    node('sync-delta').disabled = busy || !bearer || !completion || !syncComplete;
    node('sync-replay').disabled = busy || !bearer || !lastRequest;
    for (const id of ['sync-scope', 'sync-size']) node(id).disabled = busy;
    node('transfer-start').disabled = busy || !bearer;
    node('transfer-retry').disabled = busy || !bearer || !transfers.some(t => !t.verified && !t.review);
  };
  const renderSync = message => {
    node('sync-state').textContent = `${message}; ${syncComplete ? 'complete' : 'incomplete'}; applied completion version ${completionVersion}; ${(staged || jobs).size} Jobs`;
    node('sync-records').textContent = JSON.stringify(Array.from((staged || jobs).values()), null, 2);
  };
  const purgeSync = () => {
    jobs = new Map(); staged = null; completion = ''; nextRequest = ''; lastRequest = ''; syncComplete = false; completionVersion = 0;
    renderSync('Purged Job/context and continuity tokens');
  };
  // Retain only safe pending session-operation metadata for fresh-auth receipt lookup.
  const clear = () => { credentialGeneration++; bearer = ''; authenticatedUser = null; deviceCode = ''; epoch = ''; redeemOperation = ''; cancelOperation = ''; transfers = []; node('transfer-files').value = ''; node('transfer-results').textContent = ''; node('code').textContent = ''; node('sessions').textContent = ''; purgeSync(); update(); };
  async function call(path, method, body, authenticated = false, raw = false, options = {}) {
    const generation = credentialGeneration;
    const headers = {};
    if (options.operation) headers['Operation-ID'] = options.operation;
    if (body) headers['Content-Type'] = raw ? 'application/octet-stream' : 'application/json';
    if (authenticated) {
      headers.Authorization = `Bearer ${bearer}`;
      if (epoch) headers['X-FreeFSM-Session-Epoch'] = epoch;
    }
    const response = await fetch(`/api/v1${path}`, { method, headers, credentials: 'omit', cache: 'no-store', redirect: 'error', body: body ? (raw ? body : JSON.stringify(body)) : undefined });
    if (generation !== credentialGeneration) return null;
    // Denial is authoritative even when its JSON body is damaged in transit.
    if (authenticated && response.status === 401) clear();
    if (authenticated && response.status === 403) purgeSync();
    const result = response.status === 204 ? null : await response.json();
    if (response.ok && generation !== credentialGeneration) return null;
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
  // Pick known fields rather than retaining future fields in a local cache.
  // Enums are display-only here: unfamiliar values never select an action.
  const pick = (value, fields) => {
    if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('Invalid record');
    const nullable = ['start_time', 'end_time', 'due_date', 'arrival_window_start', 'arrival_window_end', 'installed_at', 'warranty_expires', 'planned_start_date', 'planned_end_date', 'latitude', 'longitude'];
    const numeric = ['latitude', 'longitude', 'progress'];
    return Object.fromEntries(fields.map(key => {
      const item = value[key];
      if (!(item === null && nullable.includes(key)) &&
          (numeric.includes(key) ? typeof item !== 'number' || !Number.isFinite(item) : key === 'archived' ? typeof item !== 'boolean' : typeof item !== 'string')) throw new Error('Invalid field');
      if (key === 'id' && !item) throw new Error('Invalid identity');
      return [key, item];
    }));
  };
  const contact = value => pick(value, ['id', 'name', 'role_title', 'phone', 'email', 'service_notes']);
  const asset = value => {
    const result = pick(value, ['id', 'number', 'name', 'ownership_class', 'type', 'status', 'serial_number', 'manufacturer', 'model', 'installed_at', 'warranty_expires', 'service_notes']);
    result.location = value.location ? pick(value.location, ['id', 'title']) : null;
    if (!['customer_owned', 'company_owned'].includes(result.ownership_class)) result.ownership_class = 'unknown';
    return result;
  };
  const job = value => {
    if (typeof value?.id !== 'string' || !value.id || typeof value.title !== 'string') throw new Error('Invalid identity');
    const result = pick(value, ['id', 'title', 'description', 'field_notes', 'start_time', 'end_time', 'due_date', 'arrival_window_start', 'arrival_window_end', 'timezone']);
    result.status = pick(value.status, ['name', 'category']);
    if (!['job:new', 'job:travel_time', 'job:in_progress', 'job:pending', 'job:completed', 'job:canceled'].includes(result.status.category)) result.status.category = 'unknown';
    if (value.customer) {
      result.customer = pick(value.customer, ['id', 'display_name', 'first_name', 'last_name', 'organization_name', 'kind', 'phone', 'email', 'service_notes']);
      if (!['Individual', 'Organization'].includes(result.customer.kind)) result.customer.kind = 'unknown';
    }
    const masters = value.masters;
    if (!masters || typeof masters !== 'object') throw new Error('Invalid context');
    result.masters = {};
    for (const [key, transform] of Object.entries({
      contact, contacts: values => values.map(contact), asset, assets: values => values.map(asset),
      location: value => pick(value, ['id', 'title', 'address_1', 'address_2', 'city', 'state', 'zip_code', 'latitude', 'longitude', 'access_notes', 'service_notes']),
      project: value => pick(value, ['name', 'description', 'status', 'progress', 'planned_start_date', 'planned_end_date', 'location', 'service_notes']),
      project_reference: value => { if (value.archived !== true) throw new Error('Unknown lifecycle'); return pick(value, ['name', 'archived']); }
    })) if (Object.hasOwn(masters, key)) result.masters[key] = transform(masters[key]);
    return result;
  };
  const syncQuery = (key, token) => {
    const params = new URLSearchParams({scope: syncScope, page_size: String(syncSize)});
    if (key) params.set(key, token);
    return `/api/v1/sync/jobs?${params}`;
  };
  async function readSync(request) {
    const generation = credentialGeneration;
    const params = new URL(request, window.location.href).searchParams;
    if (!params.has('page') && !params.has('cursor')) {
      // Tokenless replay/retry creates a NEW server materialization. Its removals
      // cannot reconcile an older cache, even when the first page was applied.
      // Reset before transport so failed requests cannot retain/mix old context.
      jobs = new Map(); staged = new Map(); lastRequest = ''; nextRequest = request;
    }
    syncComplete = false;
    renderSync('Synchronizing');
    try {
      const headers = {Authorization: `Bearer ${bearer}`};
      if (epoch) headers['X-FreeFSM-Session-Epoch'] = epoch;
      const response = await fetch(request, {headers, credentials: 'omit', cache: 'no-store', redirect: 'error'});
      if (generation !== credentialGeneration) return;
      if ([401, 403, 409].includes(response.status)) {
        // Purge before parsing: a malformed denial must not preserve old scope.
        if (response.status === 401) clear(); else purgeSync();
      }
      const envelope = await response.json();
      if (response.ok && generation !== credentialGeneration) return;
      if (!response.ok) {
        status(`HTTP ${response.status}: ${envelope?.error?.code || 'unknown_error'}; ${response.status === 409 ? 'purged; start a fresh snapshot' : 'incomplete; explicit retry or reauthorization required'}.`);
        return;
      }
      epoch = response.headers.get('X-FreeFSM-Session-Epoch') || epoch;
      const data = envelope.data;
      if (data && data.purge !== 'none') { purgeSync(); status('Unsupported purge instruction; cached context discarded. Start a fresh snapshot after protocol review.'); return; }
      if (!data || !Array.isArray(data.records) || !Array.isArray(data.removed) ||
          typeof data.complete !== 'boolean' || typeof data.watermark !== 'string' || !Number.isFinite(Date.parse(data.watermark)) ||
          (data.complete ? data.next_page !== null || typeof data.completion_cursor !== 'string' || !data.completion_cursor :
            data.completion_cursor !== null || typeof data.next_page !== 'string' || !data.next_page)) throw new Error('Unsupported page');
      if (!data.removed.every(id => typeof id === 'string' && id)) throw new Error('Invalid removal');
      // Authority removals purge both committed and previously staged pages even
      // when later replacement validation/local application fails.
      for (const id of data.removed) { jobs.delete(id); staged?.delete(id); }
      const replacement = new Map(staged || jobs);
      for (const record of data.records) { const clean = job(record); replacement.set(clean.id, clean); }
      for (const id of data.removed) replacement.delete(id);
      if (node('sync-fail').checked) { node('sync-fail').checked = false; throw new Error('Local application failed'); }
      // This pointer swap models one atomic cache+continuity transaction. A
      // persistent client must commit these together in its protected store.
      if (data.complete) {
        jobs = replacement; staged = null;
        if (completion !== data.completion_cursor) completionVersion++;
        completion = data.completion_cursor; nextRequest = ''; syncComplete = true;
      } else { staged = replacement; nextRequest = syncQuery('page', data.next_page); }
      lastRequest = request;
      renderSync(`Applied page at ${data.watermark}`);
      status('HTTP 200: page applied; no automatic next-page request.');
    } catch {
      renderSync('Response or local application unavailable; retry the same page');
      status('Synchronization incomplete; no completion cursor advanced. Explicit retry is safe.');
    }
  }
  node('sync-start').addEventListener('click', action(async () => {
    const size = Number(node('sync-size').value);
    if (!Number.isInteger(size) || size < 1 || size > 200) { status('Page size must be an integer from 1 to 200.'); return; }
    purgeSync(); syncSize = size; syncScope = node('sync-scope').value;
    await readSync(syncQuery());
  }));
  node('sync-next').addEventListener('click', action(async () => { await readSync(nextRequest); }));
  node('sync-delta').addEventListener('click', action(async () => {
    staged = new Map(jobs); nextRequest = syncQuery('cursor', completion); await readSync(nextRequest);
  }));
  node('sync-replay').addEventListener('click', action(async () => {
    // On failure retain the exact replay request for explicit retry.
    nextRequest = lastRequest; await readSync(lastRequest);
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
