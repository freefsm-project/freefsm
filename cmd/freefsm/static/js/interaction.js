/* Shared online interaction. No records, form drafts or authority are cached. */
(() => {
  if (window.freefsmInteraction) return;
  window.freefsmInteraction = true;
  try { localStorage.removeItem('htmx-history-cache'); } catch (_) {}
  const forms = new WeakMap();
  const actions = new Map();
  const owners = new WeakMap();
  const notices = new WeakMap();
  const nativeFetch = window.fetch.bind(window);
  const actionStorage = 'freefsm-unresolved-actions';
  const mobile = matchMedia('(max-width: 768px)');
  const systemTheme = matchMedia('(prefers-color-scheme: dark)');
  function applyTheme() {
    const choice = document.querySelector('[data-theme-preference]')?.dataset.themePreference || 'system';
    document.documentElement.dataset.theme = choice === 'system' ? (systemTheme.matches ? 'dark' : 'light') : choice;
  }
  systemTheme.addEventListener('change', applyTheme);
  window.freefsmReduceMapMotion = map => {
    const preference = matchMedia('(prefers-reduced-motion: reduce)');
    const update = () => {
      for (const key of ['zoomAnimation','fadeAnimation','markerZoomAnimation','inertia']) map.options[key] = !preference.matches;
      if (preference.matches) map.stop();
    };
    for (const [name, index] of [['panBy',1],['panTo',1],['flyTo',2],['flyToBounds',1],['setView',2],['setZoom',1],['fitBounds',1]]) {
      const original = map[name];
      if (original) map[name] = function(...args) { if (preference.matches) args[index] = {...args[index],animate:false,duration:0}; return original.apply(this,args); };
    }
    update(); preference.addEventListener('change',update);
    map.on('unload',()=>preference.removeEventListener('change',update));
  };
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
  const editable = form => ((form.getAttribute('method') || 'get').toLowerCase() === 'post' || form.hasAttribute('hx-post')) && form.querySelector('input:not([type=hidden]),textarea,select');
  const credentialAction = path => /^\/(login|logout|change-password|forgot-password|reset-password|accept-invite|setup|sessions|pair|users\/\d+\/sessions)(\/|$)/.test(new URL(path,location.href).pathname);
  const snapshot = form => JSON.stringify([...new FormData(form)].filter(([key]) => !['csrf_token', 'operation_id', 'save_action_id', 'save_history'].includes(key)).map(([k,v]) => [k, v instanceof File ? (v.name ? `${v.name}:${v.size}:${v.lastModified}` : '') : v]));
  const dirty = () => [...document.forms].some(form => forms.has(form) && forms.get(form) !== snapshot(form)) || [...actions.values()].some(a => a.owner?.isConnected && ['pending','unknown','review'].includes(a.state));
  const status = (form, text) => {
    let node = notices.get(form);
    if (!node?.isConnected) node = form.querySelector('[data-form-status]');
    if (!node) {
      node = document.createElement('div'); node.dataset.formStatus = ''; node.setAttribute('role', 'status'); node.setAttribute('aria-live', 'polite');
      if (form.matches('button,input,select,textarea')) form.insertAdjacentElement('afterend', node); else form.prepend(node);
    }
    notices.set(form, node);
    node.textContent = text;
    return node;
  };
  function rememberActions() {
    // Only opaque receipt identities survive navigation; never drafts, request
    // bodies, file names, record labels, prior responses or cached authority.
    try { sessionStorage.setItem(actionStorage, JSON.stringify([...actions.values()].filter(a => ['pending','unknown','review'].includes(a.state)).map(a => ({id:a.id, url:a.url, state:a.state, history:a.history})))); } catch (_) {}
  }
  function actionOwner(source) { return source?.closest('[data-action-scope],form,dialog') || source || document.querySelector('.page-body') || document.body; }
  function begin(source, id) {
    const owner = actionOwner(source);
    const previous = owners.get(owner);
    if (previous && ['pending','unknown','review'].includes(previous.state)) return null;
    const intent = owner.matches('form') ? snapshot(owner) : null;
    if (previous && intent !== null && previous.intent === intent && ['saved','completed'].includes(previous.state)) {
      showAction(previous,previous.state,previous.state === 'saved' ? 'Saved—already confirmed. Change the input before submitting another action.' : 'Request already completed. Change the input before submitting another action.');
      return null;
    }
    const field = owner.querySelector('[name=save_action_id]');
    // Rejected/confirmed intent may be revised explicitly. Unknown intent never
    // receives a replacement identity, even if another click changes its body.
    id ||= previous ? crypto.randomUUID() : field?.value || crypto.randomUUID();
    if (field) field.value = id;
    const historyTag = owner.querySelector('[name=save_history]')?.value || document.querySelector('meta[name=freefsm-save-history]')?.content || '';
    const action = {id, owner, intent, history:historyTag, pageURL:location.pathname + location.search, url: '/save-actions/' + id, state:'pending'};
    if (owner.dataset.saveStatusUrl) action.url = owner.dataset.saveStatusUrl;
    owners.set(owner, action); actions.set(id, action);
    showAction(action, 'pending', 'Saving—waiting for confirmation');
    return action;
  }
  function showAction(action, state, text) {
    action.state = state;
    const owner = action.owner;
    const node = status(owner, text); node.dataset.actionState = state;
    owner.setAttribute('aria-busy', String(state === 'pending'));
    // Inert retains input values in FormData and prevents editing uncertain
    // intent. Status/review controls remain keyboard operable.
    const controls = owner.matches('button,input,select,textarea') ? [owner] : [...(owner.matches('form') ? owner.elements : owner.querySelectorAll('input,select,textarea,button'))].filter(el => !node.contains(el));
    controls.forEach(el => {
      if (['pending','unknown','review'].includes(state)) { if (!el.inert) { el.inert = true; el.dataset.actionInert = action.id; } }
      else if (el.dataset.actionInert === action.id) { el.inert = false; delete el.dataset.actionInert; }
    });
    rememberActions();
    if (!owner.isConnected && !['pending','unknown','review'].includes(state)) actions.delete(action.id);
    return node;
  }
  function actionButton(node, label, fn) { const b = document.createElement('button'); b.type = 'button'; b.textContent = label; b.onclick = fn; node.append(b); return b; }
  function confirmForm(action) {
    if (!action.owner.matches('form')) return;
    const current=snapshot(action.owner), submitted=action.intent ?? current;
    forms.set(action.owner,submitted);
    if (current!==submitted) notices.get(action.owner)?.append(document.createTextNode(' Newer changes remain unsaved.'));
  }
  function safeURL(value) { try { const u = new URL(value, location.origin); return u.origin === location.origin && ['http:','https:'].includes(u.protocol) ? u.href : null; } catch (_) { return null; } }
  function purge(code) {
    const unresolved=[...actions.values()].filter(action=>['pending','unknown','review'].includes(action.state));
    for(const [id,action] of actions) if(!unresolved.includes(action)) actions.delete(id);
    for(const action of unresolved) { action.intent=null; delete action.retry; }
    const main = document.querySelector('.page-body') || document.body;
    main.replaceChildren();
    const p = document.createElement('p'); p.setAttribute('role','alert'); p.textContent = code === 401 ? 'Session expired—sign in again. Inaccessible input has been removed.' : 'Access is no longer available. Inaccessible input has been removed.';
    const link = document.createElement('a'); link.href = code === 401 ? '/login' : '/'; link.textContent = code === 401 ? 'Sign in' : 'Return to dashboard';
    link.dataset.recoveryNavigation = ''; link.setAttribute('hx-boost','false');
    main.append(p, link);
    for(const action of unresolved) {
      const owner=document.createElement('section');owner.className='action-recovery';main.append(owner);action.owner=owner;
      const node=showAction(action,'unknown','The unconfirmed action identity is retained for review. Inaccessible input has been removed; dismissal or sign-in does not resolve its outcome.');
      if(code!==401)actionButton(node,'Check save status',()=>reconcile(action));
    }
    rememberActions();
    const title = document.querySelector('.page-title'); if (title) title.textContent = 'Access unavailable';
  }
  async function authorizedInput(action, accessURL) {
    // Receipt ownership does not authorize retained object values. Recheck the
    // currently displayed page before exposing retained input/comparisons.
    try {
      const forgetRevokedIntent = () => {
        if (action.intent && action.owner.matches('form')) {
          const allowed=new Set([...new FormData(action.owner).keys()]);
          action.intent=JSON.stringify(JSON.parse(action.intent).filter(([name])=>allowed.has(name)));
        }
        delete action.retry;
        if (action.owner.matches('form')) forms.set(action.owner,null);
      };
      if (accessURL && safeURL(accessURL) && action.owner.isConnected && action.owner.matches('form')) {
        const access = await nativeFetch(accessURL,{cache:'no-store',headers:{Accept:'application/json','X-FreeFSM-History':action.history || ''}});
        if ([401,403,404].includes(access.status) || access.redirected) { purge(access.status === 401 || access.redirected ? 401 : 403); return false; }
        if (!access.ok) return false;
        const projection=await access.json(), editable=new Set(projection.editable_fields || []), structured=new Set(projection.structured_fields || []);
        for(const field of [...action.owner.elements]) {
          const widget=['line_items','visits','assignments','subtasks'].includes(field.name) && field.closest('fieldset');
          if(widget && !structured.has(field.name)) { widget.remove(); forgetRevokedIntent(); }
          else if(field.name && field.type!=='hidden' && !editable.has(field.name)) { (field.closest('label') || field).remove(); forgetRevokedIntent(); }
        }
      }
      const response = await nativeFetch(location.pathname + location.search, {cache:'no-store', headers:{Accept:'text/html'}});
      if ([401,403,404].includes(response.status) || (response.redirected && new URL(response.url).pathname === '/login')) { purge(response.status === 401 || response.redirected ? 401 : 403); return false; }
      if (!response.ok) return false;
      const fresh = new DOMParser().parseFromString(await response.text(), 'text/html');
      const owner = action.owner.closest('form,dialog') || action.owner;
      if (owner.matches('form')) {
        const path = form => new URL(form.getAttribute('hx-post') || form.getAttribute('action') || location.href, location.href).pathname;
        const section = owner.querySelector('[name=section]')?.value;
        const current = [...fresh.forms].find(form => path(form) === path(owner) && (!section || form.querySelector('[name=section]')?.value === section));
        if (!current) { purge(403); return false; }
        const permitted = new Set([...current.elements].filter(el=>!el.matches(':disabled') && !el.readOnly).map(el => el.name).filter(Boolean));
        for (const field of [...owner.elements]) {
          if (field.name && !permitted.has(field.name) && !['csrf_token','save_action_id','save_revision','save_history','operation_id'].includes(field.name)) {
            const widget = ['line_items','visits','assignments','subtasks'].includes(field.name) && field.closest('fieldset');
            (widget || field.closest('label') || field).remove();
            forgetRevokedIntent();
          }
        }
      } else if (owner.matches('dialog')) {
        const ref = owner.getAttribute('x-ref');
        const current = owner.id ? fresh.getElementById(owner.id) : ref ? fresh.querySelector(`dialog[x-ref="${CSS.escape(ref)}"]`) : fresh.querySelector('dialog');
        if (!current) { purge(403); return false; }
      }
      return true;
    } catch (_) { return false; }
  }
  async function reconcile(action) {
    showAction(action, 'unknown', 'Save status unknown—checking');
    try {
      const response = await nativeFetch(action.url, {headers:{Accept:'application/json','X-FreeFSM-History':action.history || ''},cache:'no-store'});
      if ([401,403].includes(response.status) || response.redirected) { purge(response.status === 403 ? 403 : 401); return; }
      if (!response.ok) throw new Error('unavailable');
      const receipt = await response.json();
      const disposition = receipt.history_changed ? 'review' : receipt.response_status === 202 ? 'pending' : receipt.disposition;
      const destination = receipt.location && safeURL(receipt.location);
      if (['saved','completed'].includes(disposition) && destination && (new URL(destination).pathname !== location.pathname || action.owner.hasAttribute('data-save-terminal') || (action.owner.matches('form') && !editable(action.owner)))) {
        // Successful archive/create/transition may intentionally remove the
        // original editor. Authorize its result instead of interpreting that
        // vanished form as permission loss, and never render old input there.
        const current = await nativeFetch(destination,{cache:'no-store',headers:{Accept:'text/html'}});
        if ([401,403,404].includes(current.status) || (current.redirected && new URL(current.url).pathname === '/login')) { action.state=disposition; purge(current.status === 401 || current.redirected ? 401 : 403); return; }
        if (!current.ok) throw new Error('result unavailable');
        showAction(action,disposition,disposition === 'saved' ? 'Saved—confirmed by the server' : 'Request completed—confirmed by the server; no change was applied');
        leaving = true; location.href = destination; return;
      }
      if (!await authorizedInput(action,receipt.input_access_url)) throw new Error('authority unavailable');
      if (disposition === 'saved' || disposition === 'completed') {
        showAction(action, disposition === 'saved' ? 'saved' : 'completed', disposition === 'saved' ? 'Saved—confirmed by the server' : 'Request completed—confirmed by the server; no change was applied');
        confirmForm(action);
        if (receipt.location && destination) {
          const node = notices.get(action.owner);
          actionButton(node, 'View confirmed result', () => { leaving = true; location.href = destination; });
          // Existing Customer provider explicitly supplies a confirmed current
          // destination, preserving its established post-save journey.
          if (action.url.startsWith('/web-actions/')) { leaving = true; location.href = destination; }
        }
        return;
      }
      const text = receipt.history_changed ? 'The original sign-in/history context is no longer available. This action may have completed before history changed. Review current records before deciding what remains to be done.' : disposition === 'pending' ? 'Pending—accepted by the server; completion has not been confirmed.' : disposition === 'not_received' ? 'The server has not received this action. Check again before deciding whether to submit the same action.' : disposition === 'rejected' ? 'Not saved. Review and revise the rejected input, or discard it.' : disposition === 'review' || disposition === 'identity_conflict' ? 'Review required. The action may have effects; compare the current authorized record before revising or discarding input.' : 'Save status unknown. Do not repeat this action while its outcome is unknown.';
      const state = ['review','identity_conflict'].includes(disposition) ? 'review' : disposition === 'rejected' ? 'rejected' : disposition === 'pending' ? 'pending' : 'unknown';
      const retrySame = receipt.retry_same_operation === true && !receipt.history_changed;
      const node = showAction(action, state, retrySame ? 'Nothing was committed. A temporary dependency failed. You may explicitly retry this same action without changing its identity or input.' : text);
      actionButton(node, 'Check save status', () => reconcile(action));
      if (receipt.review_url && safeURL(receipt.review_url)) { const a = document.createElement('a'); a.href = receipt.review_url; a.textContent = 'Review current authorized result'; node.append(a); }
      if ((disposition === 'not_received' || retrySame) && action.retry) actionButton(node, retrySame ? 'Retry same action' : 'Submit same action', async () => {
        // A second authoritative lookup immediately precedes retry; reuse the
        // captured body/identity, never whatever has since changed in the DOM.
        const check = await nativeFetch(action.url, {cache:'no-store',headers:{Accept:'application/json','X-FreeFSM-History':action.history || ''}}).catch(() => null);
        const latest = check?.ok ? await check.json() : null;
        if (latest && !latest.history_changed && (latest.disposition === 'not_received' || latest.retry_same_operation === true)) action.retry().catch(() => {}); else reconcile(action);
      });
      actionButton(node, 'Dismiss notice', () => { node.replaceChildren(document.createTextNode('Notice dismissed. The action is not resolved; do not repeat it without review.')); actionButton(node,'Check save status',()=>reconcile(action)); });
    } catch (_) {
      const node = showAction(action, 'unknown', 'Save status unknown. The server or current authorization could not be checked. Do not repeat this action.');
      actionButton(node, 'Check save status', () => reconcile(action));
    }
  }
  function retainStructuredInput(original, authorized) {
    const values = new FormData(original);
    // These are editable domain widgets, not transport/security hidden fields.
    // Only rehydrate a widget the freshly authorized form still exposes.
    for (const name of ['line_items','visits','assignments','subtasks']) {
      const field = authorized.querySelector(`input[name="${name}"]`);
      if (!field || field.matches(':disabled') || !values.has(name)) continue;
      try {
        let value = JSON.parse(values.get(name));
        if (!Array.isArray(value)) continue;
        const widget = field.closest('fieldset');
        if (widget.dataset.saveRetainedStructured === name) continue;
        if (name === 'assignments') {
          const permitted = new Set([...widget.querySelectorAll('select option')].map(option=>Number(option.value)).filter(Boolean));
          value = value.filter(assignment=>!assignment.user_id || permitted.has(Number(assignment.user_id)));
        }
        const json = JSON.stringify(value);
        if (name === 'line_items') {
          const seed = widget.querySelector('[data-line-item-json="existing"]');
          if (seed) {
            seed.textContent = json.replace(/[<>&]/g, c=>'\\u'+c.charCodeAt(0).toString(16).padStart(4,'0'));
          }
        } else if (widget.hasAttribute('data-existing')) widget.dataset.existing = json;
      } catch (_) { /* Malformed input remains a server validation concern. */ }
    }
  }
  function renderFormResponse(html, original, code) {
    const parsed = new DOMParser().parseFromString(html, 'text/html');
    if (!parsed.querySelector('form')) return false;
    let retained;
    if (original && [409,422].includes(code)) {
      const actionPath = form => new URL(form.getAttribute('action') || location.href,location.href).pathname;
      const section = original.querySelector('[name=section]')?.value;
      retained = [...parsed.forms].find(form => actionPath(form) === actionPath(original) && (!section || form.querySelector('[name=section]')?.value === section));
      if (!retained) { purge(403); return true; }
      retainStructuredInput(original,retained);
      if (!retained.querySelector('[data-error-summary]')) {
        // Some installed handlers return a fresh authorized form with a generic
        // error. Preserve only fields it still permits, not an old whole DOM.
        const data = new FormData(original);
        for (const field of retained.elements) {
          if (!field.name || field.disabled || ['password','file','submit','button'].includes(field.type) || (field.type === 'hidden' && !['revision','save_revision'].includes(field.name))) continue;
          if (['checkbox','radio'].includes(field.type)) field.checked = data.getAll(field.name).includes(field.value);
          else if (field.tagName === 'SELECT') [...field.options].forEach(o => { o.selected = data.getAll(field.name).includes(o.value); });
          else if (data.has(field.name)) field.value = data.get(field.name);
          // DOM serialization uses the content/default values, not live values.
          if (field.tagName === 'TEXTAREA') field.textContent = field.value;
          else if (field.tagName === 'SELECT') [...field.options].forEach(o=>o.toggleAttribute('selected',o.selected));
          else if (['checkbox','radio'].includes(field.type)) field.toggleAttribute('checked',field.checked);
          else field.setAttribute('value',field.value);
        }
        retained.dataset.serverError = parsed.querySelector('[role=alert],.error-msg')?.textContent.trim() || 'The server rejected this input. Review the fields before saving again.';
      }
      if ([...original.querySelectorAll('input[type=file]')].some(input=>input.files.length)) {
        summary(original,[{field:original.querySelector('input[type=file]'),message:retained.dataset.serverError || 'Upload was not saved. Review this file and its permitted fields.'}]);
        return true;
      }
    }
    const csrf = parsed.querySelector('meta[name=csrf-token]');
    if (csrf) document.querySelector('meta[name=csrf-token]')?.setAttribute('content', csrf.content);
    if (parsed.title) document.title = parsed.title;
    htmx.swap(document.body, parsed.body.innerHTML, {swapStyle:'innerHTML'});
    init();
    const errorForm = document.querySelector('form[data-server-error]');
    if (errorForm) {
      summary(errorForm,[{field:errorForm.querySelector('input:not([type=hidden]),select,textarea'),message:errorForm.dataset.serverError}]);
      forms.set(errorForm,null);
    } else document.querySelector('[data-error-summary]')?.focus();
    return true;
  }
  async function request(url, options = {}, source) {
    const owner = actionOwner(source || document.activeElement?.closest('form,dialog'));
    const action = begin(owner);
    if (!action) throw new Error(['saved','completed'].includes(owners.get(owner)?.state) ? 'This action is already confirmed. Change the input before submitting another action.' : 'This action is unresolved. Check its save status before continuing.');
    const headers = new Headers(options.headers || {}); headers.set('X-FreeFSM-Action', action.id);
    headers.set('X-FreeFSM-History', action.history);
    const revision = owner.querySelector('[name=save_revision]')?.value;
    if (revision) headers.set('X-FreeFSM-Revision',revision);
    const csrf = document.querySelector('meta[name=csrf-token]')?.content; if (csrf) headers.set('X-CSRF-Token',csrf);
    const send = async () => {
      showAction(action, 'pending', 'Saving—waiting for confirmation');
      let response;
      try { const {transport, ...init} = options; response = await (transport || nativeFetch)(url, {...init,headers}); }
      catch (_) { await reconcile(action); throw new Error(action.state === 'saved' ? 'Saved—confirmed by the server. Reload the current list to use the result; do not create it again.' : 'Save status unknown—checking. Use Check save status; do not repeat the action.'); }
      if (!owner.dataset.saveStatusUrl) action.url = response.headers.get('X-FreeFSM-Receipt') || action.url;
      if ([401,403].includes(response.status) || (response.redirected && new URL(response.url).pathname === '/login')) { purge(response.status === 403 ? 403 : 401); throw new Error('Access unavailable. Inaccessible input has been removed.'); }
      const disposition = response.headers.get('X-FreeFSM-Disposition');
      if (response.redirected || ['unknown','review','identity_conflict'].includes(disposition)) {
        await reconcile(action);
        if (!['saved','completed'].includes(action.state)) throw new Error('The action needs reconciliation. Review its status before continuing.');
        return response;
      }
      if (response.status >= 500 || response.status === 0) { await reconcile(action); return response; }
      if (response.status === 409) {
        showAction(action, 'conflict', 'Conflict—compare the current authorized record, then revise or discard your input.');
        if (response.headers.get('Content-Type')?.includes('application/json')) await reconcile(action);
        else conflictControls(action);
      } else if (!response.ok) showAction(action, 'rejected', 'Not saved. Correct the errors and submit the revised input.');
      else if (response.status === 202 || disposition === 'pending') showAction(action,'pending','Pending—accepted by the server; completion has not been confirmed.');
      else { showAction(action, disposition === 'saved' ? 'saved' : 'completed', disposition === 'saved' ? 'Saved—confirmed by the server' : 'Request completed'); confirmForm(action); }
      return response;
    };
    action.retry = send;
    return send();
  }
  function conflictControls(action) {
    const node = notices.get(action.owner);
    const link = document.createElement('a'); link.href = location.pathname + location.search; link.target = '_blank'; link.rel = 'noopener'; link.textContent = 'Compare current authorized record in a new tab'; node.append(link);
    actionButton(node, 'Discard input and reload current record', () => { leaving = true; location.reload(); });
    const explanation = document.createElement('p'); explanation.textContent = 'Your input is retained for revision. The server will recheck the original revision; use the current authorized form after comparison if the record changed.'; node.append(explanation);
  }
  window.freefsmActions = {fetch:request, reconcile, authorizedInput, accessUnavailable:purge,
    state(source) { return owners.get(actionOwner(source))?.state; },
    async check(source) { const action = owners.get(actionOwner(source)); if (action) await reconcile(action); return action?.state; },
    confirmed(source) { const owner = actionOwner(source); if (owner.matches('form')) forms.set(owner,snapshot(owner)); }
  };
  function leaveDialog(proceed) {
    if (document.querySelector('#leave-dialog[open]')) return;
    const previous = document.activeElement;
    const dialog = document.createElement('dialog'); dialog.id = 'leave-dialog'; dialog.setAttribute('aria-labelledby', 'leave-title');
    dialog.innerHTML = '<article><h2 id="leave-title">Unsaved changes</h2><p>Your changes have not been saved. Stay to finish, or discard these changes.</p><footer><button type="button" data-stay>Stay</button><button type="button" class="outline" data-discard>Discard changes</button></footer></article>';
    if ([...actions.values()].some(action=>['pending','unknown','review'].includes(action.state))) {
      dialog.querySelector('h2').textContent = 'Unconfirmed action';
      dialog.querySelector('p').textContent = 'An action is pending or its outcome is unknown. Leaving discards only local input; it does not cancel or resolve the action. Stay to check its status, or leave and check the same action when you return.';
    }
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
  function init(event) {
    leaving = false;
    changingPage = !!returnedScroll;
    applyTheme();
    for (const [id,action] of actions) if (!action.owner?.isConnected && !['pending','unknown','review'].includes(action.state)) actions.delete(id);
    const sidebar = document.getElementById('sidebar');
    if (sidebar) sidebar.inert = mobile.matches && !sidebar.classList.contains('open');
    const navigation = [...document.querySelectorAll('#sidebar a')].map(a => ({a,path:new URL(a.href).pathname}));
    const matching = navigation.filter(({path}) => location.pathname === path || (!['/','/settings'].includes(path) && location.pathname.startsWith(path + '/')));
    const currentPath = matching.sort((a,b)=>b.path.length-a.path.length)[0]?.path;
    navigation.forEach(({a,path}) => {
      const active = path === currentPath;
      a.classList.toggle('active', active);
      if (active) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current');
    });
    document.querySelectorAll('form').forEach(form => {
      if (editable(form) && !forms.has(form)) forms.set(form, form.querySelector('[data-error-summary]') || [...form.querySelectorAll('.error-msg')].some(el => !el.hidden && el.textContent.trim()) ? null : snapshot(form));
    });
    try {
      for (const saved of JSON.parse(sessionStorage.getItem(actionStorage) || '[]')) {
        if (!/^[a-f0-9-]{36}$/i.test(saved.id) || !/^\/(save-actions|web-actions)\/[a-f0-9-]+$/.test(saved.url) || actions.get(saved.id)?.owner?.isConnected) continue;
        const owner = document.createElement('section'); owner.className = 'action-recovery';
        (document.querySelector('.page-body') || document.body).prepend(owner);
        const action = actions.get(saved.id) || {...saved}; action.owner = owner; action.intent = null; delete action.retry; actions.set(action.id,action);
        const node = showAction(action, saved.state, 'An earlier action still needs reconciliation. No input has been restored.');
        actionButton(node, 'Check save status', () => reconcile(action));
        const a = document.createElement('a'); a.href = '/save-actions/'+saved.id+'/review'; a.textContent = 'Review current authorized result'; node.append(a);
      }
    } catch (_) {}
    document.querySelectorAll('.work-queue-wrap, .table-scroll').forEach(el => {
      el.tabIndex = 0; el.setAttribute('role', 'region'); if (!el.hasAttribute('aria-label')) el.setAttribute('aria-label', 'Results table, scroll for more columns');
    });
    document.querySelectorAll('dialog').forEach((dialog, index) => {
      if (!dialog.hasAttribute('aria-label') && !dialog.hasAttribute('aria-labelledby')) {
        const heading = dialog.querySelector('h1,h2,h3,h4,header strong,.modal-header strong,.item-create-dialog-header strong');
        if (heading) { heading.id ||= `dialog-title-${index}`; dialog.setAttribute('aria-labelledby', heading.id); }
      }
      if (dialog.dataset.focusManaged) return;
      dialog.dataset.focusManaged = 'true';
      let previous;
      // Capture before showModal moves focus, not after its mutation observer.
      const showModal = dialog.showModal.bind(dialog);
      dialog.showModal = () => { previous = document.activeElement; showModal(); };
      dialog.addEventListener('close', () => { if (previous?.isConnected && !dialog.contains(previous)) previous.focus(); });
    });
    document.querySelectorAll('form').forEach(form => {
      const errors = [...form.querySelectorAll('.error-msg')].filter(e => !e.hidden && e.textContent.trim());
      if (errors.length && !form.querySelector('[data-error-summary]')) summary(form, errors.map(e => ({ field: e.closest('label')?.querySelector('input,select,textarea'), message: e.textContent })));
    });
    const serverSummary = document.querySelector('[data-error-summary]:not([data-summary-focused])');
    if (serverSummary && event?.type !== 'htmx:afterSwap') { serverSummary.dataset.summaryFocused = 'true'; serverSummary.focus(); }
  }
  function resultsState(detail, failed) {
    const target = detail.target === document.body ? document.querySelector('.page-body') : detail.target;
    if (!target || target.matches('select,input,button,a,tbody,tr')) return;
    let message = target.querySelector('[data-results-state]');
    if (!message) { message = document.createElement('div'); message.dataset.resultsState = ''; message.setAttribute('role','status'); target.prepend(message); }
    message.replaceChildren();
    const text = document.createElement('p');
    text.textContent = failed ? 'Results unavailable. Previous results are not current.' : (target.querySelector('table') ? 'Updating—previous results shown until refreshed.' : 'Loading results…');
    message.append(text);
    target.setAttribute('aria-busy', String(!failed));
    if (failed) {
      // A failed refresh cannot certify that retained rows are still visible
      // under current grants. Do not redisplay them as fallback results.
      [...target.children].filter(el=>el!==message).forEach(el => { el.hidden = true; });
      const retry = document.createElement('button'); retry.type = 'button'; retry.textContent = 'Retry results';
      const url = detail.xhr?.responseURL || detail.pathInfo?.finalRequestPath || detail.requestConfig?.path;
      // Use the exact attempted URL, including filters/sort/page, not stale rows.
      retry.onclick = () => {
        if (target.matches('.focused-list-content') && url) { location.href = url; return; }
        if (detail.requestConfig?.verb === 'get' && detail.requestConfig.elt?.isConnected) {
          const source = detail.requestConfig.elt;
          if (source.hasAttribute('hx-get') && url) htmx.ajax('GET',url,{source,target});
          else if (url) location.href = url;
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
    errors.forEach(({field, message}) => {
      const item = document.createElement('li');
      if (field) {
        field.id ||= `invalid-field-${crypto.randomUUID()}`; field.setAttribute('aria-invalid', 'true');
        item.id = `${field.id}-error`;
        const help = (field.getAttribute('aria-describedby') || '').split(/\s+/).filter(id => id && !id.endsWith('-error'));
        field.setAttribute('aria-describedby', [...help,item.id].join(' '));
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
    if (forms.has(e.target.form) && (!owners.has(e.target.form) || ['saved','completed'].includes(owners.get(e.target.form).state))) status(e.target.form, forms.get(e.target.form) === snapshot(e.target.form) ? 'No unsaved changes' : 'Unsaved changes');
  });
  document.addEventListener('change', e => {
    if (forms.has(e.target.form) && (!owners.has(e.target.form) || ['saved','completed'].includes(owners.get(e.target.form).state))) status(e.target.form, forms.get(e.target.form) === snapshot(e.target.form) ? 'No unsaved changes' : 'Unsaved changes');
  });
  document.addEventListener('click', e => {
    const link = e.target.closest('a[href]');
    if (!link || link.target || link.hasAttribute('download') || e.ctrlKey || e.metaKey || e.shiftKey || link.getAttribute('href').startsWith('#')) return;
    if (link.hasAttribute('data-recovery-navigation')) { leaving = true; return; }
    if (!leaving && dirty()) { e.preventDefault(); e.stopImmediatePropagation(); leaveDialog(() => { location.href = link.href; }); }
    else rememberScroll();
  }, true);
  window.addEventListener('beforeunload', e => { if (!leaving && dirty()) { e.preventDefault(); e.returnValue = ''; } });
  document.addEventListener('submit', async e => {
    if (e.defaultPrevented || (e.target.getAttribute('method') || 'get').toLowerCase() !== 'post') return;
    const form = e.target;
    // Providers with their own accepted/pending/result protocol attach a form
    // listener and preventDefault first. Native forms get retained-input fetch.
    if (form.getAttribute('hx-boost') !== 'false' || form.hasAttribute('hx-post')) return;
    if (credentialAction(form.action) || form.closest('#backup-ui') || !form.querySelector('[name=save_action_id]')) { leaving = true; return; }
    e.preventDefault();
    // Native guarded forms use the same declarative confirmation as HTMX.
    // Check before allocating an action identity or capturing/sending input.
    const confirmation = form.getAttribute('hx-confirm');
    if (confirmation && !window.confirm(confirmation)) return;
    const data = new FormData(form, e.submitter);
    const body = form.enctype === 'multipart/form-data' ? data : new URLSearchParams(data);
    const url = e.submitter?.getAttribute('formaction') || form.action;
    try {
      const response = await request(url, {method:'POST',body,headers:{'HX-Request':'true'}}, form);
      if (!form.isConnected) return;
      if (response.status === 422 || response.status === 409) {
        const html = await response.text();
        if (response.headers.get('Content-Type')?.includes('text/html') && renderFormResponse(html,form,response.status)) return;
        if (response.status === 422) summary(form,[{message:'The server rejected this input. Review the fields and try the corrected action.'}]);
        return;
      }
      if (!response.ok || response.status === 202) return;
      const redirect = response.headers.get('HX-Redirect') || (response.redirected ? response.url : null);
      if (redirect && safeURL(redirect)) { leaving = true; location.href = redirect; return; }
      if (response.headers.get('Content-Type')?.includes('text/html')) renderFormResponse(await response.text());
    } catch (_) { /* The durable action notice owns transport errors. */ }
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
  document.addEventListener('htmx:configRequest', e => {
    if (e.detail.verb === 'get' || credentialAction(e.detail.path)) return;
    const action = begin(e.detail.elt);
    if (!action) { e.preventDefault(); return; }
    e.detail.headers['X-FreeFSM-Action'] = action.id;
    e.detail.headers['X-FreeFSM-History'] = action.history;
    if (e.detail.parameters.save_action_id) e.detail.parameters.save_action_id = action.id;
    if (e.detail.parameters.save_history) e.detail.parameters.save_history = action.history;
  });
  document.addEventListener('htmx:beforeHistoryUpdate', e => {
    if (e.detail.xhr.status < 400 || e.detail.requestConfig?.verb === 'get') return;
    const action = owners.get(actionOwner(e.detail.requestConfig?.elt));
    if (action?.pageURL) {
      // An error form is still the editor, not a successful navigation to its
      // POST destination. Preserve Back context and a reloadable editor URL.
      e.detail.history.path = action.pageURL;
      e.detail.history.type = 'replace';
    }
  });
  document.addEventListener('htmx:beforeRequest', e => {
    if (e.detail.target === document.body) { rememberScroll(); changingPage = true; }
    if (e.detail.requestConfig.verb === 'get') resultsState(e.detail, false);
  });
  document.addEventListener('htmx:afterRequest', e => {
    const owner = actionOwner(e.detail.requestConfig?.elt || e.detail.elt);
    const action = owners.get(owner);
    const disposition = e.detail.xhr.getResponseHeader('X-FreeFSM-Disposition');
    if (action && e.detail.xhr.status >= 200 && e.detail.xhr.status < 400 && disposition) {
      const pending = e.detail.xhr.status === 202 || disposition === 'pending';
      if (['unknown','review'].includes(disposition)) { reconcile(action); return; }
      showAction(action, pending ? 'pending' : disposition === 'saved' ? 'saved' : 'completed', pending ? 'Pending—accepted by the server; completion has not been confirmed.' : disposition === 'saved' ? 'Saved—confirmed by the server' : 'Request completed');
      if (!pending) confirmForm(action);
    }
  });
  document.addEventListener('htmx:beforeSwap', e => {
    const code = e.detail.xhr.status;
    if (code >= 400 && e.detail.requestConfig.verb === 'get' && ![401,403,404].includes(code)) resultsState(e.detail, true);
    const owner = actionOwner(e.detail.requestConfig.elt);
    const action = owners.get(owner);
    if (action && e.detail.requestConfig.verb !== 'get' && code >= 200 && code < 400 && !e.detail.xhr.getResponseHeader('X-FreeFSM-Disposition')) {
      e.detail.shouldSwap = false;
      reconcile(action).then(() => {
        if (['saved','completed'].includes(action.state) && e.detail.xhr.responseURL && safeURL(e.detail.xhr.responseURL)) { leaving = true; location.href = e.detail.xhr.responseURL; }
      });
      return;
    }
    const html = e.detail.xhr.getResponseHeader('Content-Type')?.includes('text/html');
    if ((code === 422 || (code === 409 && (e.detail.xhr.getResponseHeader('X-FreeFSM-Form') === 'review' || e.detail.xhr.responseText.includes('data-error-summary')))) && html) {
      if (action) { action.state = 'rejected'; rememberActions(); }
      e.detail.shouldSwap = true; e.detail.isError = false;
    }
    if ([401,403,404].includes(code)) {
      e.detail.shouldSwap = false; purge(code);
    } else if (code >= 400 && action && !e.detail.shouldSwap) {
      if (code >= 500 || (code === 409 && !html)) reconcile(action);
      else { showAction(action, code === 409 ? 'conflict' : 'rejected', code === 409 ? 'Conflict—review the current record before revising or discarding your changes.' : 'Request failed. Your changes have not been confirmed.'); if (code === 409) conflictControls(action); }
    }
  });
  function transportError(e) {
    if (e.detail.requestConfig?.verb === 'get') { resultsState(e.detail, true); return; }
    const action = owners.get(actionOwner(e.detail.requestConfig?.elt || e.detail.elt));
    if (action) reconcile(action);
  }
  document.addEventListener('htmx:sendError', transportError);
  document.addEventListener('htmx:timeout', transportError);
  document.addEventListener('htmx:afterSwap', init);
  document.addEventListener('htmx:afterSettle', init);
  document.addEventListener('DOMContentLoaded', init);
  if (document.readyState !== 'loading') init();
})();
