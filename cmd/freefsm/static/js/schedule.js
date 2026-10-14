(function () {
    'use strict';
    const workspace = document.getElementById('schedule-workspace');
    if (!workspace || workspace.dataset.bound === 'true') return;
    workspace.dataset.bound = 'true';
    let authorizedHistoryReturn = false;
    try {
        const returned = JSON.parse(sessionStorage.getItem('freefsm-return') || 'null');
        authorizedHistoryReturn = returned && returned.url === location.pathname + location.search;
    } catch (_) {}
    if (workspace.dataset.phoneDefault === 'true' && matchMedia('(max-width: 640px)').matches) {
        location.replace('/schedule?tab=list&period=day&responsive=1');
        return;
    }
    // The cookie initializes a new visit only. Bind this document to the server's
    // normalized query so another tab cannot retarget its results on refresh.
    const queryURL = new URL(workspace.dataset.queryUrl, location.href);
    // Refresh/remembered navigation must not change saved display preferences.
    queryURL.searchParams.set('responsive', '1');
    if (!location.search) history.replaceState(history.state, '', queryURL);
    // Handlers belong to the workspace lifecycle, not a successful result
    // fragment. Initial error documents and refreshed nodes use the same code;
    // response scripts are never evaluated to restore interactions.
    window.scheduleOpenMonthDay = function (event, target) {
        if (event.defaultPrevented || event.target.closest('a')) return;
        if (target.dataset.dayHref) location.href = target.dataset.dayHref;
    };
    window.scheduleOpenMonthDayKey = function (event, target) {
        if (event.defaultPrevented || event.target.closest('a')) return;
        if (event.key !== 'Enter' && event.key !== ' ') return;
        event.preventDefault();
        if (target.dataset.dayHref) location.href = target.dataset.dayHref;
    };
    window.scheduleCalendarDrag = window.scheduleDispatchDrag = function (event) {
        event.dataTransfer.setData('text/plain', event.currentTarget.dataset.jobId);
    };
    window.scheduleCalendarDragOver = window.scheduleDispatchDragOver = function (event) {
        event.preventDefault();
        event.currentTarget.classList.add('drag-over');
    };
    window.scheduleCalendarDragLeave = window.scheduleDispatchDragLeave = function (event) {
        event.currentTarget.classList.remove('drag-over');
    };
    function proposeDrop(event, target, dispatch) {
        event.preventDefault();
        target.classList.remove('drag-over');
        const jobID = event.dataTransfer.getData('text/plain');
        if (!jobID || !target.dataset.date || dispatch && !target.dataset.userId) return;
        const proposal = new URLSearchParams({date: target.dataset.date});
        if (dispatch) proposal.set('user_id', target.dataset.userId);
        if (target.dataset.hour) proposal.set('hour', target.dataset.hour);
        location.href = '/jobs/' + encodeURIComponent(jobID) + '/dispatch?' + proposal;
    }
    window.scheduleCalendarDrop = function (event, target) { proposeDrop(event, target, false); };
    window.scheduleDispatchDrop = function (event, target) { proposeDrop(event, target, true); };
    const results = workspace.querySelector('#schedule-results');
    const state = workspace.querySelector('#schedule-load-state');
    const retry = workspace.querySelector('#schedule-retry');
    const refresh = workspace.querySelector('#schedule-refresh');
    let generation = 0;
    let controller;
    function saveScroll() {
        if (!document.body.contains(workspace) || !results.childElementCount) return;
        const containers = {};
        results.querySelectorAll('[data-schedule-scroll]').forEach(el => {
            containers[el.dataset.scheduleScroll] = {x: el.scrollLeft, y: el.scrollTop};
        });
        try { sessionStorage.setItem('schedule-scroll:' + location.href, JSON.stringify({x: scrollX, y: scrollY, containers})); } catch (_) {}
    }
    function restoreScroll() {
        try {
            const saved = JSON.parse(sessionStorage.getItem('schedule-scroll:' + location.href) || 'null');
            if (!saved) return;
            if (typeof saved === 'number') { scrollTo(0, saved); return; }
            results.querySelectorAll('[data-schedule-scroll]').forEach(el => {
                const position = (saved.containers || {})[el.dataset.scheduleScroll];
                if (position) { el.scrollLeft = Number(position.x) || 0; el.scrollTop = Number(position.y) || 0; }
            });
            scrollTo(Number(saved.x) || 0, Number(saved.y) || 0);
        } catch (_) {}
    }
    function clear() {
        generation++;
        if (controller) controller.abort();
        const map = results.querySelector('#schedule-map');
        if (map && map.freefsmMap) map.freefsmMap.remove();
        results.replaceChildren();
    }
    async function update() {
        if (!document.body.contains(workspace)) return;
        clear();
        const current = generation;
        controller = new AbortController();
        state.textContent = 'Updating schedule — loading current authorized Jobs…';
        results.setAttribute('aria-busy', 'true');
        retry.hidden = true;
        try {
            const response = await fetch(queryURL, {
                credentials: 'same-origin', cache: 'no-store', signal: controller.signal,
                headers: {'X-Schedule-Refresh': 'true'}
            });
            const text = await response.text();
            if (current !== generation) return;
            if (!response.ok || response.redirected) throw new Error('unavailable');
            const doc = new DOMParser().parseFromString(text, 'text/html');
            const fresh = doc.getElementById('schedule-results');
            if (!fresh) throw new Error('unavailable');
            results.replaceChildren(...fresh.childNodes);
            if (window.htmx) window.htmx.process(results);
            state.textContent = doc.getElementById('schedule-load-state').textContent;
            if (window.initScheduleMap) window.initScheduleMap();
        } catch (_) {
            if (current !== generation) return;
            results.replaceChildren();
            state.textContent = 'Schedule unavailable. Retry to recheck access and load current Jobs.';
            retry.hidden = false;
        } finally {
            if (current === generation) results.removeAttribute('aria-busy');
        }
    }
    refresh.addEventListener('click', update);
    retry.addEventListener('click', update);
    // Persist numeric positions before the shared interaction layer purges the
    // page on navigation. Its purge may precede this module's pagehide callback.
    results.addEventListener('scroll', saveScroll, {capture: true, passive: true});
    window.addEventListener('scroll', saveScroll, {passive: true});
    workspace.addEventListener('click', saveScroll, {capture: true});
    workspace.addEventListener('submit', saveScroll, {capture: true});
    // No authority-bearing HTML is kept in a back/forward snapshot. Only scroll
    // and server-validated presentation state survive navigation.
    window.addEventListener('pagehide', function () {
        if (!document.body.contains(workspace)) return;
        saveScroll();
        clear();
    }, {capture: true});
    window.addEventListener('pageshow', function (event) { if (event.persisted) update().then(restoreScroll); });
    document.addEventListener('visibilitychange', function () {
        if (!document.body.contains(workspace)) return;
        if (document.hidden) {
            saveScroll();
            clear();
            state.textContent = 'Loading schedule on return…';
        } else update().then(restoreScroll);
    });
    const navigation = performance.getEntriesByType('navigation')[0];
    if (authorizedHistoryReturn || navigation && navigation.type === 'back_forward') {
        restoreScroll();
    }
})();
