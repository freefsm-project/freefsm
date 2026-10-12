/* Shared online interaction. No records, form drafts or authority are cached. */
(() => {
  if (window.freefsmInteraction) return;
  window.freefsmInteraction = true;
  try { localStorage.removeItem('htmx-history-cache'); } catch (_) {}
  const forms = new WeakMap();
  const mobile = matchMedia('(max-width: 768px)');
  const systemTheme = matchMedia('(prefers-color-scheme: dark)');
  function applyTheme() {
    const choice = document.querySelector('[data-theme-preference]')?.dataset.themePreference || 'system';
    document.documentElement.dataset.theme = choice === 'system' ? (systemTheme.matches ? 'dark' : 'light') : choice;
  }
  systemTheme.addEventListener('change', applyTheme);
  let leaving = false;
  let menuFocus;
  let position = history.state?.freefsmPosition || 0;
  const pushState = history.pushState.bind(history);
  const replaceState = history.replaceState.bind(history);
  replaceState({ ...history.state, freefsmPosition: position }, '');
  history.pushState = (state, title, url) => { position++; pushState({ ...state, freefsmPosition: position }, title, url); };
  history.replaceState = (state, title, url) => replaceState({ ...state, freefsmPosition: position }, title, url);
  let undoBack;
  let changingPage = false;
  let returnedScroll;
  try { returnedScroll = JSON.parse(sessionStorage.getItem('freefsm-return')); } catch (_) {}
  window.addEventListener('popstate', e => {
    if (undoBack) {
      e.stopImmediatePropagation();
      const proceed = undoBack; undoBack = null;
      changingPage = false;
      leaveDialog(proceed);
      return;
    }
    const target = e.state?.freefsmPosition;
    if (!leaving && dirty() && Number.isInteger(target) && target !== position) {
      e.stopImmediatePropagation();
      changingPage = true;
      const delta = target - position;
      undoBack = () => history.go(delta);
      history.go(-delta);
      return;
    }
    if (Number.isInteger(target)) position = target;
    // Always fetch the returned page under current authority. Neither HTMX's
    // snapshot cache nor a restored DOM may resurrect revoked records or input.
    e.stopImmediatePropagation();
    changingPage = true;
    try { sessionStorage.setItem('freefsm-return', JSON.stringify({url: location.pathname + location.search, scroll: JSON.parse(sessionStorage.getItem(scrollKey()))})); } catch (_) {}
    location.reload();
  }, true);
  const scrollKey = () => 'freefsm-scroll:' + location.pathname + location.search;
  function rememberScroll() {
    if (changingPage) return;
    try { sessionStorage.setItem(scrollKey(), JSON.stringify({ x: scrollX, y: scrollY })); } catch (_) { /* Storage can be disabled. */ }
  }
  function restoreScroll() {
    try { const p = JSON.parse(sessionStorage.getItem(scrollKey())); if (p) scrollTo(p.x, p.y); } catch (_) { /* No persisted records or input. */ }
  }
  window.addEventListener('scroll', rememberScroll, { passive: true });
  window.addEventListener('pageshow', e => {
    if (e.persisted) { location.reload(); return; }
    try { sessionStorage.removeItem('freefsm-return'); } catch (_) {}
    if (returnedScroll?.url === location.pathname + location.search && returnedScroll.scroll) {
      const saved = returnedScroll.scroll;
      requestAnimationFrame(() => { scrollTo(saved.x,saved.y); returnedScroll = null; changingPage = false; });
    } else if (performance.getEntriesByType('navigation')[0]?.type === 'back_forward') requestAnimationFrame(restoreScroll);
  });
  window.addEventListener('pagehide', () => { document.querySelector('.main-content')?.replaceChildren(); });
  document.addEventListener('htmx:historyRestore', restoreScroll);
  const editable = form => form.method.toLowerCase() === 'post' && form.querySelector('input:not([type=hidden]),textarea,select');
  const snapshot = form => JSON.stringify([...new FormData(form)].filter(([key]) => !['csrf_token', 'operation_id'].includes(key)).map(([k,v]) => [k, v instanceof File ? (v.name ? `${v.name}:${v.size}:${v.lastModified}` : '') : v]));
  const dirty = () => [...document.forms].some(form => forms.has(form) && forms.get(form) !== snapshot(form));
  const status = (form, text) => {
    let node = form.querySelector('[data-form-status]');
    if (!node) {
      node = document.createElement('p'); node.dataset.formStatus = ''; node.setAttribute('role', 'status');
      form.prepend(node);
    }
    node.textContent = text;
  };
  function leaveDialog(proceed) {
    if (document.querySelector('#leave-dialog[open]')) return;
    const previous = document.activeElement;
    const dialog = document.createElement('dialog'); dialog.id = 'leave-dialog'; dialog.setAttribute('aria-labelledby', 'leave-title');
    dialog.innerHTML = '<article><h2 id="leave-title">Unsaved changes</h2><p>Your changes have not been saved. Stay to finish, or discard these changes.</p><footer><button type="button" data-stay>Stay</button><button type="button" class="outline" data-discard>Discard changes</button></footer></article>';
    document.body.append(dialog);
    const stay = () => { dialog.close(); dialog.remove(); if (previous?.isConnected) previous.focus(); };
    dialog.querySelector('[data-stay]').onclick = stay;
    dialog.addEventListener('cancel', e => { e.preventDefault(); stay(); });
    dialog.querySelector('[data-discard]').onclick = () => { leaving = true; dialog.close(); dialog.remove(); proceed(); };
    dialog.showModal(); dialog.querySelector('[data-stay]').focus();
  }
  window.freefsmMenu = function(open) {
    const sidebar = document.getElementById('sidebar');
    const button = document.querySelector('.hamburger');
    if (!sidebar || !button) return;
    open = open === undefined ? !sidebar.classList.contains('open') : open;
    sidebar.classList.toggle('open', open);
    sidebar.inert = mobile.matches && !open;
    button.setAttribute('aria-expanded', String(open));
    document.querySelector('.main-content').inert = mobile.matches && open;
    if (open) { menuFocus = document.activeElement; sidebar.querySelector('input, a, button').focus(); }
    else if (menuFocus?.isConnected) { menuFocus.focus(); menuFocus = null; }
  };
  function init() {
    leaving = false;
    changingPage = !!returnedScroll;
    applyTheme();
    const sidebar = document.getElementById('sidebar');
    if (sidebar) sidebar.inert = mobile.matches && !sidebar.classList.contains('open');
    document.querySelectorAll('#sidebar a').forEach(a => {
      const path = new URL(a.href).pathname;
      const active = path === '/' ? location.pathname === '/' : location.pathname === path || location.pathname.startsWith(path + '/');
      a.classList.toggle('active', active);
      if (active) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current');
    });
    document.querySelectorAll('form').forEach(form => {
      if (editable(form) && !forms.has(form)) forms.set(form, [...form.querySelectorAll('.error-msg')].some(el => !el.hidden && el.textContent.trim()) ? null : snapshot(form));
    });
    document.querySelectorAll('.work-queue-wrap, .table-scroll').forEach(el => {
      el.tabIndex = 0; el.setAttribute('role', 'region'); el.setAttribute('aria-label', 'Results table, scroll for more columns');
    });
    document.querySelectorAll('dialog').forEach((dialog, index) => {
      if (!dialog.hasAttribute('aria-label') && !dialog.hasAttribute('aria-labelledby')) {
        const heading = dialog.querySelector('h1,h2,h3,h4,header strong,.modal-header strong,.item-create-dialog-header strong');
        if (heading) { heading.id ||= `dialog-title-${index}`; dialog.setAttribute('aria-labelledby', heading.id); }
      }
      if (dialog.dataset.focusManaged) return;
      dialog.dataset.focusManaged = 'true';
      let previous;
      new MutationObserver(() => {
        if (dialog.open) { previous = document.activeElement; }
      }).observe(dialog, { attributes: true, attributeFilter: ['open'] });
      dialog.addEventListener('close', () => { if (previous?.isConnected && !dialog.contains(previous)) previous.focus(); });
    });
    document.querySelectorAll('form').forEach(form => {
      const errors = [...form.querySelectorAll('.error-msg')].filter(e => !e.hidden && e.textContent.trim());
      if (errors.length && !form.querySelector('[data-error-summary]')) summary(form, errors.map(e => ({ field: e.closest('label')?.querySelector('input,select,textarea'), message: e.textContent })));
    });
  }
  function resultsState(detail, failed) {
    const target = detail.target;
    if (!target?.matches('.focused-list-content, .shared-widget')) return;
    let message = target.querySelector('[data-results-state]');
    if (!message) { message = document.createElement('div'); message.dataset.resultsState = ''; message.setAttribute('role','status'); target.prepend(message); }
    message.replaceChildren();
    const text = document.createElement('p');
    text.textContent = failed ? 'Results unavailable. Previous results are not current.' : (target.querySelector('table') ? 'Updating—previous results shown until refreshed.' : 'Loading results…');
    message.append(text);
    target.setAttribute('aria-busy', String(!failed));
    if (failed) {
      const retry = document.createElement('button'); retry.type = 'button'; retry.textContent = 'Retry results';
      const url = detail.xhr?.responseURL || detail.pathInfo?.finalRequestPath || detail.requestConfig?.path;
      // Use the exact attempted URL, including filters/sort/page, not stale rows.
      retry.onclick = () => {
        if (target.matches('.focused-list-content') && url) { location.href = url; return; }
        if (detail.requestConfig?.verb === 'get' && detail.requestConfig.elt?.isConnected) {
          const source = detail.requestConfig.elt;
          const form = source.closest('form');
          if (form) { form.requestSubmit(); return; }
          htmx.trigger(source, 'load');
        } else if (url) location.href = url;
      };
      message.append(retry);
    }
  }
  function summary(form, errors) {
    form.querySelector('[data-error-summary]')?.remove();
    const box = document.createElement('div'); box.dataset.errorSummary = ''; box.className = 'error-summary'; box.tabIndex = -1; box.setAttribute('role', 'alert');
    const title = document.createElement('h2'); title.textContent = 'Please correct the following'; box.append(title);
    const list = document.createElement('ul'); box.append(list);
    errors.forEach(({field, message}, i) => {
      const item = document.createElement('li');
      if (field) {
        field.id ||= `invalid-field-${i}`; field.setAttribute('aria-invalid', 'true');
        item.id = `${field.id}-error`; field.setAttribute('aria-describedby', item.id);
        const link = document.createElement('a'); link.href = `#${field.id}`; link.textContent = message;
        link.onclick = e => { e.preventDefault(); field.focus(); }; item.append(link);
      } else item.textContent = message;
      list.append(item);
    });
    form.prepend(box); box.focus();
  }
  document.addEventListener('invalid', e => {
    if (!e.target.form) return;
    e.preventDefault();
    const form = e.target.form;
    queueMicrotask(() => summary(form, [...form.elements].filter(f => f.willValidate && !f.validity.valid).map(field => ({ field, message: `${field.labels?.[0]?.textContent.trim() || field.name}: ${field.validationMessage}` }))));
  }, true);
  document.addEventListener('input', e => {
    if (forms.has(e.target.form)) status(e.target.form, forms.get(e.target.form) === snapshot(e.target.form) ? 'No unsaved changes' : 'Unsaved changes');
  });
  document.addEventListener('change', e => {
    if (forms.has(e.target.form)) status(e.target.form, forms.get(e.target.form) === snapshot(e.target.form) ? 'No unsaved changes' : 'Unsaved changes');
  });
  document.addEventListener('click', e => {
    const link = e.target.closest('a[href]');
    if (!link || link.target || link.hasAttribute('download') || e.ctrlKey || e.metaKey || e.shiftKey || link.getAttribute('href').startsWith('#')) return;
    if (!leaving && dirty()) { e.preventDefault(); e.stopImmediatePropagation(); leaveDialog(() => { location.href = link.href; }); }
    else rememberScroll();
  }, true);
  window.addEventListener('beforeunload', e => { if (!leaving && dirty()) { e.preventDefault(); e.returnValue = ''; } });
  document.addEventListener('submit', e => {
    if (e.defaultPrevented || e.target.method.toLowerCase() !== 'post') return;
    // Native navigation still receives a server response; input remains dirty on rejection.
    if (e.target.getAttribute('hx-boost') === 'false') leaving = true;
  });
  document.addEventListener('keydown', e => {
    const sidebar = document.querySelector('#sidebar.open');
    if (!sidebar || !mobile.matches) return;
    if (e.key === 'Escape') { e.preventDefault(); window.freefsmMenu(false); }
    if (e.key === 'Tab') {
      const controls = [...sidebar.querySelectorAll('a,button,input,summary')].filter(el => el.getClientRects().length);
      const first = controls[0], last = controls.at(-1);
      if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
      else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
    }
  });
  mobile.addEventListener('change', () => window.freefsmMenu(false));
  document.addEventListener('htmx:beforeRequest', e => {
    const form = e.detail.elt.closest('form');
    if (form && e.detail.requestConfig.verb !== 'get') {
      if (form.getAttribute('aria-busy') === 'true') { e.preventDefault(); return; }
      form.setAttribute('aria-busy', 'true'); status(form, 'Saving—waiting for confirmation');
    }
    if (e.detail.requestConfig.verb === 'get') resultsState(e.detail, false);
    if (e.detail.target === document.body) { rememberScroll(); changingPage = true; }
  });
  document.addEventListener('htmx:afterRequest', e => {
    const form = e.detail.elt.closest('form');
    if (form) form.removeAttribute('aria-busy');
  });
  document.addEventListener('htmx:beforeSwap', e => {
    const code = e.detail.xhr.status;
    if (code >= 400 && e.detail.requestConfig.verb === 'get' && ![401,403,404].includes(code)) resultsState(e.detail, true);
    if (code === 422 || (code === 409 && e.detail.xhr.getResponseHeader('X-FreeFSM-Form') === 'review')) { e.detail.shouldSwap = true; e.detail.isError = false; }
    if ([401,403,404].includes(code)) {
      const title = document.querySelector('.page-title'); if (title) title.textContent = 'Access unavailable';
      const main = document.querySelector('.page-body');
      if (main) { main.replaceChildren(); const p = document.createElement('p'); p.setAttribute('role', 'alert'); p.textContent = code === 401 ? 'Session expired—sign in again' : 'Access is no longer available. Inaccessible input has been removed.'; main.append(p); const a = document.createElement('a'); a.href = code === 401 ? '/login' : '/'; a.textContent = code === 401 ? 'Sign in' : 'Return to dashboard'; main.append(a); }
    } else if (code >= 400 && code !== 422) {
      const form = e.detail.requestConfig.elt.closest('form');
      if (form) status(form, code === 409 ? 'Conflict—review the current record before revising or discarding your changes.' : 'Request failed. Your changes have not been confirmed.');
    }
  });
  document.addEventListener('htmx:sendError', e => {
    if (e.detail.requestConfig?.verb === 'get') { resultsState(e.detail, true); return; }
    const form = e.detail.elt.closest('form');
    if (!form) return;
    status(form, 'Save status unknown—checking');
    const statusURL = form.dataset.saveStatusUrl;
    if (!statusURL) { status(form, 'Save status unknown—checking is unavailable for this action. Verify the current record before repeating it.'); return; }
    const controls = [...form.querySelectorAll('input,select,textarea,button')];
    controls.forEach(el => { el.inert = true; });
    async function reconcile() {
      status(form, 'Save status unknown—checking');
      try {
        const response = await fetch(statusURL, { headers: { Accept: 'application/json' }, cache: 'no-store' });
        if (!response.ok || response.redirected) {
          if ([401,403,404].includes(response.status) || response.redirected) { leaving = true; location.href = '/login'; return; }
          throw new Error('unavailable');
        }
        const receipt = await response.json();
        if (receipt.disposition === 'saved') {
          const destination = new URL(receipt.location,location.origin);
          if (destination.origin !== location.origin) throw new Error('invalid receipt destination');
          status(form, 'Saved—confirmed by the server');
          leaving = true;
          location.href = destination.href;
          return;
        }
        status(form, 'Save status remains unknown. Check again or retry the same action; it cannot create a second effect.');
        const retry = document.createElement('button'); retry.type = 'button'; retry.textContent = 'Retry same action';
        retry.onclick = () => { controls.forEach(el => { el.inert = false; }); form.requestSubmit(); };
        form.querySelector('[data-form-status]').append(retry);
      } catch (_) { status(form, 'Save status unknown. The server could not be reached.'); }
      const check = document.createElement('button'); check.type = 'button'; check.textContent = 'Check save status'; check.onclick = reconcile;
      form.querySelector('[data-form-status]').append(check);
    }
    reconcile();
  });
  document.addEventListener('htmx:afterSwap', init);
  document.addEventListener('htmx:afterSettle', init);
  document.addEventListener('DOMContentLoaded', init);
  if (document.readyState !== 'loading') init();
})();
