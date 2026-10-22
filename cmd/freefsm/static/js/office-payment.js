(() => {
  'use strict';
  const key = 'freefsm.office-payment.pending-operation';
  function setup() {
    const form = document.querySelector('form[data-office-payment]');
    if (!form || form.dataset.ready) return;
    form.dataset.ready = 'true';
    const status = document.querySelector('[data-office-payment-status]');
    const check = document.querySelector('[data-office-payment-check]');
    const next = document.querySelector('[data-office-payment-new]');
    let pending, busy = false;
    const lock = () => form.querySelectorAll('input,select,textarea,button').forEach(el => { el.disabled = true; });
    const unknown = () => { lock(); status.textContent = 'Receipt status unknown. Check the original receipt before recording more money.'; check.hidden = false; };
    function denied(response) {
      if (![401,403].includes(response.status) && !(response.redirected && new URL(response.url).pathname === '/login')) return false;
      form.reset(); lock();
      if (window.freefsmActions) window.freefsmActions.accessUnavailable(response.status === 403 ? 403 : 401);
      else document.querySelector('.main-content')?.replaceChildren();
      return true;
    }
    function accept(receipt) {
      if (!receipt || receipt.operation_id !== pending || receipt.outcome !== 'accepted' ||
          !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(receipt.payment_id) ||
          !Number.isFinite(Date.parse(receipt.accepted_at))) { unknown(); return; }
      lock(); status.textContent = 'Receipt accepted. Do not record this money again.'; check.hidden = true; next.hidden = false;
    }
    next.addEventListener('click', event => {
      event.preventDefault();
      if (!busy && confirm('Have you reconciled the original receipt? Record another receipt only for additional money actually received.')) {
        sessionStorage.removeItem(key); location.assign(form.action);
      }
    });
    check.addEventListener('click', async () => {
      if (busy || !pending) return;
      busy = true;
      try {
        const response = await fetch('/payment-operations/'+pending, {cache:'no-store',headers:{Accept:'application/json'}});
        if (denied(response)) return;
        // Receipt ownership does not authorize retained Customer input. Refresh
        // current office action context before displaying a financial outcome.
        const current = await fetch(form.action,{cache:'no-store'});
        if (denied(current)) return;
        if (!current.ok) { form.reset(); unknown(); return; }
        if (current.headers.get('X-FreeFSM-Office-Payment') !== 'allowed') {
          form.reset(); lock();
          if (window.freefsmActions) window.freefsmActions.accessUnavailable(403);
          else document.querySelector('.main-content')?.replaceChildren();
          return;
        }
        if (response.ok) accept((await response.json()).data);
        else if (response.status === 404) {
          status.textContent = 'No retained receipt. Reconcile recorded history, including any restore, before another receipt.';
          next.hidden = false;
        } else unknown();
      } catch { unknown(); } finally { busy = false; }
    });
    try { pending = sessionStorage.getItem(key); if (pending) unknown(); }
    catch { lock(); status.textContent = 'Enable session storage before recording money.'; return; }
    form.addEventListener('submit', async event => {
      event.preventDefault();
      if (busy || pending) return;
      const body = new URLSearchParams(new FormData(form));
      pending = body.get('idempotency_key');
      try { sessionStorage.setItem(key,pending); }
      catch { pending = null; status.textContent = 'Receipt not submitted: reconciliation storage unavailable.'; return; }
      busy = true; lock(); status.textContent = 'Recording receipt…';
      try {
        const response = await fetch(form.action,{method:'POST',body,headers:{Accept:'application/json'}});
        if (denied(response)) return;
        if (response.ok) accept((await response.json()).data);
        else unknown();
      } catch { unknown(); } finally { busy = false; }
    });
  }
  setup(); document.addEventListener('htmx:afterSwap',setup);
})();
