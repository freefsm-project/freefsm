(() => {
  'use strict';
  const recovery = document.getElementById('subtask-recovery');
  if (!recovery || recovery.dataset.installed) return;
  recovery.dataset.installed = 'true';
  const output = document.getElementById('subtask-outcome');
  const forms = [...document.querySelectorAll('form[data-subtask-command]')];
  const endpoint = recovery.dataset.endpoint;
  // Tab-local, user/Job-scoped recovery survives reload without exposing another
  // user's saved intent. CSRF is transport evidence, not operation intent.
  const key = `freefsm:subtask-intent:${recovery.dataset.owner}:${endpoint}`;
  let pending = null;
  let busy = false;
  let submittedForm = null;
  let accessible = true;
  const lock = () => {
    for (const form of forms) {
      form.dataset.pending = 'true';
      for (const control of form.elements) control.disabled = true;
      if (pending) form.querySelector('[name=operation_id]').value = pending.operation;
    }
  };
  const clear = () => { sessionStorage.removeItem(key); pending = null; };
  const reviewLink = () => {
    const link = document.createElement('a');
    link.href = accessible ? endpoint : '/jobs';
    link.textContent = accessible ? 'Reload and review current checklist' : 'Return to authorized Jobs';
    recovery.append(link);
  };
  const purge = () => {
    accessible = false;
    if (pending) {
      // Retain only nonsensitive identity for receipt reconciliation. Access
      // loss removes the original intent, so it can no longer be replayed.
      pending = {operation:pending.operation, intent:null};
      sessionStorage.setItem(key, JSON.stringify(pending));
    }
    submittedForm = null;
    // A denial must not leave cached Job/history/input on screen.
    for (const form of forms) form.replaceChildren();
    document.body.replaceChildren(output, recovery);
    recovery.replaceChildren();
    if (pending) controls('Checklist access unavailable. Only the original receipt can be checked.');
    reviewLink();
  };
  const terminal = code => {
    if (['forbidden', 'not_found'].includes(code)) purge();
    clear();
    recovery.replaceChildren();
    output.textContent = `Original operation: ${code}. Reload to inspect current authorized checklist.`;
    if (code === 'accepted' && submittedForm) window.freefsmActions?.confirmed(submittedForm);
    reviewLink();
  };
  const controls = message => {
    recovery.replaceChildren();
    output.textContent = `${message} Original operation: ${pending.operation}. Reconcile before creating new intent.`;
    for (const [label, action] of [['Check original operation', inspect], ['Retry unchanged original operation', submit]]) {
      if (action === submit && pending.intent === null) continue;
      const button = document.createElement('button');
      button.type = 'button'; button.textContent = label;
      button.addEventListener('click', action);
      recovery.append(button);
    }
  };
  // Revalidate the current parent and signed-in user before showing a result or
  // replaying saved intent. Never render a cached sensitive representation.
  const authorize = async () => {
    const response = await fetch(endpoint, {cache:'no-store', credentials:'same-origin', redirect:'follow'});
    if (response.redirected || [401,403,404].includes(response.status)) { purge(); return false; }
    if (!response.ok) throw new Error('Checklist unavailable');
    const current = new DOMParser().parseFromString(await response.text(), 'text/html').getElementById('subtask-recovery');
    if (!current || current.dataset.owner !== recovery.dataset.owner) { purge(); return false; }
    recovery.dataset.csrf = current.dataset.csrf;
    return true;
  };
  async function inspect() {
    if (busy || !pending) return;
    busy = true;
    recovery.replaceChildren();
    try {
      // A safe receipt remains useful after losing the parent. authorize purges
      // sensitive content on denial, but leaves the receipt identity intact.
      if (pending.intent !== null) await authorize();
      const response = await fetch(`/subtask-operations/${pending.operation}`, {cache:'no-store', credentials:'same-origin', redirect:'error'});
      if ([401,403].includes(response.status)) { purge(); return; }
      if (response.ok) {
        const receipt = await response.json();
        if (receipt.operation_id !== pending.operation || !receipt.code) throw new Error('Invalid receipt');
        terminal(receipt.code); return;
      }
      controls('No terminal receipt available. Outcome remains unknown.');
    } catch {
      if (pending) controls('Save status remains unknown.');
    } finally { busy = false; }
  }
  async function submit() {
    if (busy || !pending || pending.intent === null) return;
    busy = true;
    recovery.replaceChildren();
    output.textContent = 'Saving checklist action…';
    try {
      if (!await authorize()) return;
      const body = new URLSearchParams(pending.intent);
      body.set('csrf_token', recovery.dataset.csrf);
      const response = await fetch(endpoint, {method:'POST', body, credentials:'same-origin', redirect:'error', headers:{Accept:'application/json'}});
      if ([401,403,404].includes(response.status)) { purge(); return; }
      if (response.ok) {
        const receipt = await response.json();
        if (receipt.operation_id !== pending.operation || receipt.code !== 'accepted') throw new Error('Unconfirmed response');
        clear();
        if (submittedForm) window.freefsmActions?.confirmed(submittedForm);
        location.reload(); return;
      }
      // Even a validation response is reconciled against the immutable receipt;
      // a proxy/server error must never silently permit a fresh UUID.
    } catch { /* Lost POST response: retain the exact intent and identity. */ }
    finally { busy = false; }
    if (pending) await inspect();
  }
  try {
    const saved = sessionStorage.getItem(key);
    if (saved) {
      pending = JSON.parse(saved);
      if (!pending.operation || (pending.intent !== null && new URLSearchParams(pending.intent).get('operation_id') !== pending.operation)) throw new Error('Invalid recovery');
      lock();
      controls('Save status remains unknown.');
    }
  } catch {
    lock();
    output.textContent = 'Checklist recovery storage unavailable. No new action can be submitted safely.';
    return;
  }
  for (const form of forms) form.addEventListener('submit', async event => {
    if (event.defaultPrevented) return;
    event.preventDefault();
    if (pending || busy) return;
    const confirmation = form.getAttribute('hx-confirm');
    if (confirmation && !window.confirm(confirmation)) return;
    const data = new URLSearchParams(new FormData(form));
    if (event.submitter) data.set('action', event.submitter.value);
    data.delete('csrf_token');
    pending = {operation:data.get('operation_id'), intent:data.toString()};
    try { sessionStorage.setItem(key, JSON.stringify(pending)); }
    catch {
      pending = null;
      output.textContent = 'Checklist recovery storage unavailable. Nothing was submitted.';
      return;
    }
    submittedForm = form;
    lock();
    await submit();
  });
})();
