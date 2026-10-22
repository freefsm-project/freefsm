(() => {
  'use strict';
  const key = 'freefsm.office-payment.pending-operation';
  function setup() {
    let marker = document.querySelector('[data-office-payment-page]');
    if (!marker || marker.dataset.ready) return;
    marker.dataset.ready = 'true';
    const main = marker.closest('.main-content');
    const pagePath = location.pathname;
    let form, status, check, next, pending, busy = false;
    const live = () => main.isConnected && main.contains(marker) && location.pathname === pagePath;
    const lock = () => form?.querySelectorAll('input,select,textarea,button').forEach(el => { el.disabled = true; });
    function bind() {
      form = main.querySelector('form[data-office-payment]');
      status = main.querySelector('[data-office-payment-status]');
      check = main.querySelector('[data-office-payment-check]');
      next = main.querySelector('[data-office-payment-new]');
      check.onclick = inspect;
      next.onclick = () => {
        if (!busy && form && confirm('Have you reconciled the original receipt? Record another receipt only for additional money actually received.')) {
          sessionStorage.removeItem(key); location.assign(pagePath);
        }
      };
      if (form) form.onsubmit = submit;
    }
    // Invalidate the whole sensitive projection, not just the entry fields.
    // Neither a receipt nor payment.credit authorizes old history/Invoice View.
    function withhold() {
      if (!live()) return false;
      form?.reset();
      document.title = 'Payment receipt reconciliation';
      marker = document.createElement('h1');
      marker.textContent = 'Payment receipt reconciliation';
      marker.dataset.officePaymentPage = ''; marker.dataset.ready = 'true';
      const notice = document.createElement('p');
      notice.textContent = 'Customer content is withheld until refreshed with current access. Receipt ownership permits only the original nonsensitive outcome.';
      status = document.createElement('p'); status.dataset.officePaymentStatus = ''; status.setAttribute('role','status');
      check = document.createElement('button'); check.type = 'button'; check.dataset.officePaymentCheck = ''; check.textContent = 'Check original receipt';
      next = document.createElement('button'); next.type = 'button'; next.dataset.officePaymentNew = ''; next.textContent = 'Review a different receipt'; next.hidden = true;
      main.replaceChildren(marker,notice,status,check,next);
      bind(); check.disabled = busy;
      return true;
    }
    async function refresh() {
      const owner = marker;
      const current = await fetch(pagePath,{cache:'no-store'});
      if (!live() || owner !== marker) return;
      if (!current.ok || current.redirected && new URL(current.url).pathname !== pagePath) return;
      const doc = new DOMParser().parseFromString(await current.text(),'text/html');
      if (!live() || owner !== marker) return;
      const fresh = doc.querySelector('.main-content');
      const freshMarker = fresh?.querySelector('[data-office-payment-page]');
      if (!freshMarker || !fresh.querySelector('[data-office-payment-check]')) return;
      // Only this live server projection can restore sensitive content. Never
      // merge it with old options, input, receipt history, or cached HTML.
      marker = freshMarker; marker.dataset.ready = 'true';
      main.replaceChildren(...fresh.childNodes);
      document.title = doc.title;
      bind(); lock(); check.disabled = busy;
      window.htmx?.process(main);
    }
    function unknown() {
      if (!live()) return;
      lock(); status.textContent = 'Receipt status unknown. Check the original receipt before recording more money.';
      check.hidden = false; next.hidden = true;
    }
    async function outcome(response) {
      if (!live()) return;
      if (response.ok && !response.redirected) {
        const receipt = (await response.json()).data;
        if (!live()) return;
        if (receipt?.operation_id === pending && receipt.outcome === 'accepted' &&
            /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(receipt.payment_id) &&
            Number.isFinite(Date.parse(receipt.accepted_at))) {
          lock(); status.textContent = 'Receipt accepted. Do not record this money again.';
          check.hidden = !!form; next.hidden = !form; return;
        }
      } else if (response.status === 404) {
        status.textContent = 'No retained receipt. Reconcile recorded history, including any restore, before another receipt.';
        check.hidden = false; next.hidden = !form; return;
      }
      unknown();
    }
    async function inspect() {
      if (busy || !pending || !live()) return;
      busy = true;
      if (!withhold()) { busy = false; return; }
      status.textContent = 'Checking original receipt…';
      try {
        const response = await fetch('/payment-operations/'+pending,{cache:'no-store',headers:{Accept:'application/json'}});
        // Failure/denial/404 leaves the neutral shell. The original user's safe
        // receipt remains queryable independently of Customer/facet access.
        try { await refresh(); } catch { /* sensitive content stays withheld */ }
        await outcome(response);
      } catch { unknown(); } finally { busy = false; if (live()) check.disabled = false; }
    }
    async function submit(event) {
      event.preventDefault();
      if (busy || pending || !form || !live()) return;
      const body = new URLSearchParams(new FormData(form));
      const action = form.action;
      pending = body.get('idempotency_key');
      try { sessionStorage.setItem(key,pending); }
      catch { pending = null; status.textContent = 'Receipt not submitted: reconciliation storage unavailable.'; return; }
      busy = true;
      if (!withhold()) { busy = false; return; }
      status.textContent = 'Recording receipt…';
      try {
        const response = await fetch(action,{method:'POST',body,headers:{Accept:'application/json'}});
        try { await refresh(); } catch { /* no stale projection fallback */ }
        // Only the receipt lookup's404 means a missing receipt. A denied POST
        // cannot resolve an uncertain original operation or authorize a replay.
        if (response.ok) await outcome(response); else unknown();
      } catch { unknown(); } finally { busy = false; if (live()) check.disabled = false; }
    }
    bind();
    try { pending = sessionStorage.getItem(key); if (pending) unknown(); }
    catch { lock(); status.textContent = 'Enable session storage before recording money.'; }
  }
  setup(); document.addEventListener('htmx:afterSwap',setup);
})();
