(() => {
  'use strict';
  const key = 'freefsm.payment.pending-operation';
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
        sessionStorage.removeItem(key); pending = null; form.reset();
        form.elements.idempotency_key.value = crypto.randomUUID(); lock(false); panel.replaceChildren();
      }
      function accepted() {
        lock(true); message('Payment accepted. The original receipt was confirmed; do not record it again.');
        button('Record a different Payment', () => { if (confirm('The original Payment is accepted. Start a separate Payment only for additional money actually received?')) newIntent(); });
      }
      async function inspect() {
        if (busy || !pending) return;
        busy = true;
        try {
          const response = await fetch('/payment-operations/' + pending, { cache: 'no-store', headers: { Accept: 'application/json' } });
          if (response.ok) {
            const receipt = (await response.json()).data;
            if (receipt.operation_id !== pending) throw new Error('Unexpected receipt');
            if (receipt.outcome === 'accepted') accepted();
            else {
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
          if (response.ok) {
            const receipt = (await response.json()).data;
            if (receipt.operation_id !== pending || receipt.outcome !== 'accepted') throw new Error('Unexpected receipt');
            accepted();
          } else { unknown(); }
        } catch { unknown(); } finally { busy = false; }
      });
    });
  }
  setup(); document.addEventListener('htmx:afterSwap', setup);
})();
