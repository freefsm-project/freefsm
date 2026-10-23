(() => {
  'use strict';
  const key = 'freefsm.credit-application.pending-operation';
  function setup() {
    let marker = document.querySelector('[data-credit-page]');
    if (!marker || marker.dataset.ready) return;
    marker.dataset.ready = 'true';
    const main = marker.closest('.main-content'), path = location.pathname;
    let pending, busy = false, status, check, next;
    const live = () => main.isConnected && main.contains(marker) && location.pathname === path;
    const lock = () => main.querySelectorAll('form[data-credit-command] input,form[data-credit-command] select,form[data-credit-command] textarea,form[data-credit-command] button').forEach(el => { el.disabled = true; });
    function bind() {
      status = main.querySelector('[data-credit-status]');
      check = main.querySelector('[data-credit-check]');
      next = main.querySelector('[data-credit-new]');
      main.querySelectorAll('form[data-credit-command]').forEach(form => { form.onsubmit = submit; });
      check.onclick = inspect;
      next.onclick = () => {
        if (!busy && confirm('Have you reconciled the original operation? Continue only for a different Application or reversal.')) {
          sessionStorage.removeItem(key); location.assign(path);
        }
      };
    }
    function withhold() {
      if (!live()) return;
      main.querySelectorAll('form').forEach(form => form.reset());
      document.title = 'Credit operation reconciliation';
      marker = document.createElement('h1'); marker.dataset.creditPage = ''; marker.dataset.ready = 'true';
      marker.textContent = 'Credit operation reconciliation';
      const notice = document.createElement('p'); notice.textContent = 'Invoice, source and history content is withheld until refreshed with current access.';
      status = document.createElement('p'); status.dataset.creditStatus = ''; status.setAttribute('role','status');
      check = document.createElement('button'); check.type = 'button'; check.dataset.creditCheck = ''; check.textContent = 'Check original operation';
      next = document.createElement('button'); next.type = 'button'; next.dataset.creditNew = ''; next.textContent = 'Start a different operation'; next.hidden = true;
      main.replaceChildren(marker,notice,status,check,next); bind(); check.disabled = busy;
    }
    async function refresh() {
      const owner = marker;
      const response = await fetch(path,{cache:'no-store'});
      if (!live() || owner !== marker || !response.ok || response.redirected) return;
      const doc = new DOMParser().parseFromString(await response.text(),'text/html');
      if (!live() || owner !== marker) return;
      const fresh = doc.querySelector('.main-content'), freshMarker = fresh?.querySelector('[data-credit-page]');
      if (!freshMarker || !fresh.querySelector('[data-credit-check]')) return;
      marker = freshMarker; marker.dataset.ready = 'true'; main.replaceChildren(...fresh.childNodes); document.title = doc.title;
      bind(); lock(); check.disabled = busy; window.htmx?.process(main);
    }
    function unknown() {
      if (!live()) return;
      lock(); status.textContent = 'Operation status unknown. Check the original operation before another attempt.'; check.hidden = false; next.hidden = true;
    }
    async function reconcile() {
      // Receipt first, then a new authorized projection. Neither the POST nor
      // the receipt is permission to restore old source options or history.
      const response = await fetch('/credit-application-operations/'+pending,{cache:'no-store',headers:{Accept:'application/json'}});
      let accepted = false;
      if (response.ok && !response.redirected) {
        const data = (await response.json()).data;
        accepted = data?.operation_id === pending && data.outcome === 'accepted';
      }
      try { await refresh(); } catch { /* remain neutral on denied/offline refresh */ }
      if (!live()) return;
      if (accepted) {
        status.textContent = 'Operation accepted. Do not repeat it.'; check.hidden = false; next.hidden = !main.querySelector('form[data-credit-command]');
      } else if (response.status === 404) {
        status.textContent = 'No retained operation. Reconcile history, including any restore, before starting a different operation.';
        check.hidden = false; next.hidden = !main.querySelector('form[data-credit-command]');
      } else { unknown(); }
    }
    async function inspect() {
      if (busy || !pending || !live()) return;
      busy = true; withhold(); status.textContent = 'Checking original operation…';
      try { await reconcile(); } catch { unknown(); }
      finally { busy = false; if (live()) check.disabled = false; }
    }
    async function submit(event) {
      event.preventDefault();
      if (busy || pending || !live()) return;
      const form = event.currentTarget, body = new URLSearchParams(new FormData(form)), action = form.action;
      pending = body.get('idempotency_key');
      try { sessionStorage.setItem(key,pending); }
      catch { pending = null; status.textContent = 'Not submitted: reconciliation storage unavailable.'; return; }
      busy = true; withhold(); status.textContent = 'Submitting Credit operation…';
      try {
        await fetch(action,{method:'POST',body,headers:{Accept:'application/json'}});
        await reconcile();
      } catch { unknown(); }
      finally { busy = false; if (live()) check.disabled = false; }
    }
    bind();
    try { pending = sessionStorage.getItem(key); if (pending) { withhold(); inspect(); } }
    catch { lock(); status.textContent = 'Enable session storage before a Credit operation.'; }
  }
  setup(); document.addEventListener('htmx:afterSwap',setup);
})();
