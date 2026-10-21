(() => {
  'use strict';
  const key = 'freefsm.payment.pending-operation';
  const terminalOutcomes = new Set(['not_found', 'forbidden', 'invalid_payment', 'invoice_ineligible']);
  const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
  function setup() {
    document.querySelectorAll('form[data-bounded-payment]').forEach(form => {
      if (form.dataset.paymentReady) return;
      form.dataset.paymentReady = 'true';
      const panel = form.parentElement.querySelector('[data-payment-reconciliation]');
      let pending = null;
      let busy = false;
      const eligible = form.dataset.paymentEligible === 'true';
      function lock(value) { form.querySelectorAll('input,select,button').forEach(el => { el.disabled = value || !eligible; }); }
      function button(label, action) { const b = document.createElement('button'); b.type = 'button'; b.textContent = label; b.onclick = action; panel.append(b); }
      function message(text) { panel.replaceChildren(); const p = document.createElement('p'); p.textContent = text; panel.append(p); }
      function newIntent() {
        if (busy) return;
        sessionStorage.removeItem(key);
        // Reload current authorized context. The server issues a fresh domain
        // identity AND guarded-save identity/history/revision together.
        location.reload();
      }
      function accepted() {
        lock(true); message('Payment accepted. The original receipt was confirmed; do not record it again.');
        button('Record a different Payment', () => { if (confirm('The original Payment is accepted. Start a separate Payment only for additional money actually received?')) newIntent(); });
      }
      function unrecognized() {
        lock(true); message('Unrecognized Payment receipt. Keep the original identity locked for review.');
        button('Check original Payment receipt', inspect);
      }
      function validReceipt(receipt) {
        return receipt && receipt.operation_id === pending && typeof receipt.accepted_at === 'string' && Number.isFinite(Date.parse(receipt.accepted_at)) &&
          (receipt.outcome === 'accepted' ? typeof receipt.payment_id === 'string' && uuidPattern.test(receipt.payment_id) : terminalOutcomes.has(receipt.outcome) && receipt.payment_id === undefined);
      }
      function accessLost(status) {
        // Use the shared browser purge contract. Keep only the opaque operation
        // identity, never amount/reference/note or a cached Invoice representation.
        if (window.freefsmActions) window.freefsmActions.accessUnavailable(status);
        else document.querySelector('.main-content')?.replaceChildren();
        const link=document.createElement('a');link.href='/payment-operations/'+pending;
        link.textContent='After authentication, inspect the original Payment receipt';link.setAttribute('hx-boost','false');
        document.querySelector('.main-content')?.append(link);
      }
      function denied(response) {
        if ([401,403].includes(response.status) || response.redirected && new URL(response.url).pathname === '/login') {
          accessLost(response.status === 403 ? 403 : 401);return true;
        }
        return false;
      }
      async function inspect() {
        if (busy || !pending) return;
        busy = true;
        try {
          const response = await fetch('/payment-operations/' + pending, { cache: 'no-store', headers: { Accept: 'application/json' } });
          if (denied(response)) return;
          if (window.freefsmActions && !await window.freefsmActions.authorizedInput({owner:form,history:form.elements.save_history?.value || ''})) return;
          if (response.status === 200) {
            const receipt = (await response.json()).data;
            if (!validReceipt(receipt)) {unrecognized();return;}
            if (receipt.outcome === 'accepted') accepted();
            else {
              if (['forbidden','not_found'].includes(receipt.outcome)) {accessLost(403);return;}
              message('Payment rejected. Review the current Invoice before creating a new intent.');
              button('Review a new Payment', newIntent);
            }
          } else if (response.status === 404) {
            message('No retained receipt. The outcome remains uncertain, especially after restore. Reconcile recorded history before another Payment.');
            button('Check original Payment receipt', inspect);
            button('Confirm reconciled — review a new Payment', () => { if (confirm('Have you explicitly reconciled whether this money was already recorded, including restore uncertainty? Never repeat an uncertain Payment.')) newIntent(); });
          } else { unknown(); }
        } catch { unknown(); } finally { busy = false; }
      }
      function unknown() {
        lock(true); message('Payment status unknown. Check the original receipt before another Payment.');
        button('Check original Payment receipt', inspect);
      }
      try { pending = sessionStorage.getItem(key); if (pending) unknown(); }
      catch { lock(true); message('Payment reconciliation storage is unavailable. Enable session storage before recording a Payment.'); return; }
      form.addEventListener('submit', async event => {
        event.preventDefault();
        if (pending || busy || !eligible) return;
        const body = new URLSearchParams(new FormData(form));
        pending = body.get('idempotency_key');
        try { sessionStorage.setItem(key, pending); }
        catch { pending = null; message('Payment was not submitted: reconciliation storage is unavailable.'); return; }
        busy = true; lock(true); message('Recording Payment…');
        try {
          const response = await fetch(form.action, { method: 'POST', body, headers: { Accept: 'application/json' } });
          if (denied(response)) return;
          if (response.status === 404) {accessLost(403);return;}
          if (response.status === 200) {
            const receipt = (await response.json()).data;
            if (!validReceipt(receipt) || receipt.outcome !== 'accepted') {unrecognized();return;}
            accepted();
          } else { unknown(); }
        } catch { unknown(); } finally { busy = false; }
      });
    });
  }
  setup(); document.addEventListener('htmx:afterSwap', setup);
})();
