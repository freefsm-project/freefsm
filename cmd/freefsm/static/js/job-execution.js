(() => {
  'use strict';
  if (window.freefsmJobExecutionInstalled) return;
  window.freefsmJobExecutionInstalled = true;
  document.addEventListener('submit', async event => {
    const form = event.target.closest('form[data-execution-form]');
    if (!form) return;
    event.preventDefault(); event.stopImmediatePropagation();
    if (form.dataset.busy) return;
    form.dataset.busy = 'true';
    let data = new URLSearchParams(new FormData(form));
    const operation = data.get('operation_id');
    const button = form.querySelector('button[type=submit]');
    button.disabled = true;
    const message = document.createElement('div');
    message.setAttribute('role', 'status'); form.append(message);
    const review = () => {
      const link = document.createElement('a'); link.href = data ? location.pathname : '/jobs';
      link.textContent = data ? 'Review current values before a new submission' : 'Return to authorized Jobs';
      message.append(document.createElement('br'), link);
    };
    const purge = () => {
      data = null;
      // Remove both live values and default text, including an earlier conflict
      // comparison or Job context elsewhere on the page. Retain only the
      // nonsensitive operation identity needed for receipt reconciliation.
      for (const field of form.querySelectorAll('textarea,input,select')) { field.value = ''; field.textContent = ''; }
      form.replaceChildren();
      document.body.replaceChildren(message);
      document.title = 'Job access unavailable';
    };
    const terminal = code => {
      message.textContent = code === 401 ? 'Session expired—sign in again. No automatic retry.' : `Save not accepted (${code}). No automatic retry.`;
      if (data && !['not_found','forbidden',401,403,404].includes(code)) {
        const compare = document.createElement('button');
        compare.type = 'button'; compare.textContent = 'Compare current values and review';
        compare.onclick = async () => {
          compare.disabled = true;
          try {
            const response = await fetch(location.pathname, {cache:'no-store',redirect:'error'});
            if ([401,403,404].includes(response.status)) {purge();terminal('access_lost');return;}
            if (!response.ok) throw new Error('unavailable');
            const documentNow = new DOMParser().parseFromString(await response.text(),'text/html');
            const fresh = documentNow.querySelector('form[data-execution-form]');
            if (!fresh) {purge();terminal('access_lost');return;}
            const comparison = document.createElement('section');
            const heading = document.createElement('h3');heading.textContent='Review current and proposed values';comparison.append(heading);
            const notes = fresh.querySelector('[name=field_notes]');
            const current = document.createElement('pre');current.style.whiteSpace='pre-wrap';
            if (notes) {
              current.textContent=`Current Field Notes:\n${notes.value}\n\nYour proposed Field Notes:\n${data.get('field_notes')}`;
              notes.value=data.get('field_notes');notes.textContent=notes.value;
            } else {
              current.textContent='Status identity or meaning may have changed. Explicitly select the intended Status again below.';
              const select=fresh.querySelector('[name=status_id]');
              for (const option of select.options) option.removeAttribute('selected');
              const placeholder=documentNow.createElement('option');placeholder.value='';placeholder.textContent='Explicitly reselect Status';placeholder.setAttribute('selected','');placeholder.disabled=true;
              select.prepend(placeholder);select.required=true;
            }
            comparison.append(current);
            fresh.querySelector('button[type=submit]').textContent=notes?'Save reviewed Field Notes':'Save explicitly reselected Status';
            comparison.append(document.importNode(fresh,true));
            form.replaceWith(comparison);
          } catch {
            message.textContent='Current values unavailable. No repair was submitted.';
            review();
          }
        };
        message.append(compare);
      }
      review();
      const receiptLink=document.createElement('a');receiptLink.href=`/job-execution-operations/${operation}`;
      receiptLink.textContent=`Check original operation receipt (${operation})`;
      message.append(document.createElement('br'),receiptLink);
    };
    const inspect = async () => {
      try {
        const response = await fetch(`/job-execution-operations/${operation}`, { cache: 'no-store', redirect: 'error' });
        if (response.ok) {
          const receipt = await response.json();
          if (receipt.code === 'accepted') { location.assign(location.pathname); return; }
          if (['not_found','forbidden'].includes(receipt.code)) purge();
          terminal(receipt.code); return;
        }
        if ([401,403].includes(response.status)) { purge(); terminal('access_lost'); return; }
        message.textContent = 'No terminal receipt available. The outcome is still unknown; keep this operation ID.';
      } catch { message.textContent = 'Receipt unavailable. The outcome is still unknown.'; }
      reconciliationButtons();
    };
    const reconciliationButtons = () => {
      const check = document.createElement('button'); check.type = 'button'; check.textContent = 'Check original operation';
      check.onclick = inspect; message.append(check);
      const retry = document.createElement('button'); retry.type = 'button'; retry.textContent = 'Retry unchanged original operation';
      retry.onclick = submit; if (data) message.append(retry);
    };
    const submit = async () => {
      if (!data) return;
      try {
        const response = await fetch(form.action, { method: 'POST', body: data, headers: { 'X-CSRF-Token': data.get('csrf_token') || '' }, redirect: 'follow', cache: 'no-store' });
        if (response.ok && response.redirected) { location.assign(response.url); return; }
        if ([401,403,404].includes(response.status)) purge();
        if ([400,401,403,404,409].includes(response.status)) { terminal(response.status); return; }
      } catch { /* Lost response: query the original identity. */ }
      message.textContent = 'Response unavailable. Outcome unknown; reconcile the original operation before changing intent.';
      reconciliationButtons();
    };
    await submit();
  }, true);
})();
