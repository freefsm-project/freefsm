const {chromium} = require('playwright-core');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const [base, reader, admin, alpha, beta, worker, scenario, out] = process.argv.slice(2);
function record(kind, data) {
    const row = JSON.stringify({kind, ...data});
    console.log(row);
    fs.appendFileSync(out + '/observations.jsonl', row + '\n');
}
(async () => {
    const browser = await chromium.launch({executablePath: process.env.BROWSER_EXECUTABLE, args: ['--no-sandbox']});
    const errors = [];
    try {
        const context = await browser.newContext({viewport: {width: 1280, height: 900}});
        await context.addCookies([{name: 'session', value: scenario === 'query' ? reader : admin, url: base}]);
        async function page(label) {
            const p = await context.newPage();
            p.setDefaultTimeout(10000);
            p.on('pageerror', e => { errors.push(e.message); record('pageerror', {label, message: e.message}); });
            p.on('response', r => {
                if (new URL(r.url()).pathname === '/schedule' || new URL(r.url()).pathname.includes('/dispatch')) {
                    record('http', {label, url: r.url(), status: r.status(), refresh: r.request().headers()['x-schedule-refresh'] || null});
                }
            });
            return p;
        }
        async function goto(p, path, status = 200) {
            assert.equal((await p.goto(base + path)).status(), status);
            await p.waitForFunction(() => document.querySelector('#schedule-workspace')?.dataset.bound === 'true');
        }
        async function refresh(p) {
            const response = p.waitForResponse(r => r.request().headers()['x-schedule-refresh'] === 'true');
            await p.locator('#schedule-refresh').click();
            assert.equal((await response).status(), 200);
            await p.waitForFunction(() => !document.querySelector('#schedule-results').hasAttribute('aria-busy'));
        }
        async function state(p) {
            return p.evaluate(() => ({filters: Object.fromEntries(new FormData(document.querySelector('.schedule-filters'))), title: document.querySelector('.schedule-title-nav h3').textContent, links: [...document.querySelectorAll('.schedule-toolbar a')].map(a => a.getAttribute('href')), text: document.querySelector('#schedule-results').innerText, list: !!document.querySelector('.schedule-list-day')}));
        }
        // Headless Chromium does not hide tabs on bringToFront. Dispatch the real
        // visibility lifecycle with a controlled visibility property, not a fetch
        // or response mock; every refresh still uses production HTTP and the DB.
        async function visibility(p, hidden) {
            await p.evaluate(hidden => {
                Object.defineProperty(document, 'hidden', {configurable: true, value: hidden});
                document.dispatchEvent(new Event('visibilitychange'));
            }, hidden);
        }
        async function returnTo(p) {
            const response = p.waitForResponse(r => r.request().headers()['x-schedule-refresh'] === 'true');
            await visibility(p, false);
            assert.equal((await response).status(), 200);
            await p.waitForFunction(() => !document.querySelector('#schedule-results').hasAttribute('aria-busy'));
        }
        if (scenario === 'query') {
            const A = '/schedule?tab=list&period=day&date=2026-10-20&q=Alpha&status=open&assignment=assigned&sort=title';
            const B = '/schedule?tab=calendar&period=day&date=2026-10-21&q=Beta&status=finished&assignment=assigned&sort=due';
            const a = await page('A'), b = await page('B');
            await goto(a, A); await goto(a, '/schedule');
            const before = await state(a);
            assert.deepEqual(before.filters, {tab: 'list', period: 'day', date: '2026-10-20', q: 'Alpha', status: 'open', assignment: 'assigned', sort: 'title'});
            async function coherent() {
                const now = await state(a);
                assert.deepEqual(now.filters, before.filters);
                assert.equal(now.title, before.title);
                assert.deepEqual(now.links, before.links);
                assert.match(now.text, /Alpha recovery/);
                assert.doesNotMatch(now.text, /Beta recovery/);
                assert.equal(now.list, true);
            }
            await goto(b, B); assert.match((await state(b)).text, /Beta recovery/);
            await refresh(a); await coherent();
            await visibility(a, true);
            assert.equal(await a.locator('#schedule-results a').count(), 0);
            await goto(b, B); await returnTo(a); await coherent();
            await a.reload(); await coherent();
            await goto(a, B); await goto(b, B); await a.goBack();
            await a.waitForFunction(() => document.querySelector('.schedule-filters [name=q]')?.value === 'Alpha');
            await coherent();
            await a.screenshot({path: out + '/query-coherent.png', fullPage: true});
            await visibility(a, true);
            assert.equal((await context.request.get(base + '/fixture/revoke')).status(), 204);
            await goto(b, B); await returnTo(a);
            assert.equal(await a.locator(`#schedule-results a[href="/jobs/${alpha}"]`).count(), 0);
            assert.match((await state(a)).text, /No matching Jobs/);
            assert.deepEqual((await state(a)).filters, before.filters);
            record('query-contract', {sharedCookieRefresh: true, visibilityReturn: true, reload: true, back: true, assignmentLoss: true});
        } else {
            async function recover(p, path, method) {
                assert.equal((await context.request.get(base + '/fixture/fault?on=1')).status(), 204);
                try {
                    await goto(p, path, 503);
                    assert.match(await p.locator('#schedule-results').innerText(), /Schedule unavailable/);
                    assert.equal(await p.locator('[data-job-id]').count(), 0);
                } finally {
                    assert.equal((await context.request.get(base + '/fixture/fault?on=0')).status(), 204);
                }
                if (method === 'refresh') await refresh(p);
                else { await visibility(p, true); await returnTo(p); }
                await p.locator(`[data-job-id="${alpha}"]`).first().waitFor();
                assert.match(await p.locator('#schedule-load-state').innerText(), /Updated at/);
                assert.equal(await p.locator('.schedule-period-tabs a').count(), 3, 'recovery must restore period navigation');
                record('recovered', {path, method, periodLinks: 3});
            }
            for (const method of ['refresh', 'return']) {
                for (const v of [{tab: 'calendar', period: 'day'}, {tab: 'calendar', period: 'month'}, {tab: 'dispatch', period: 'day'}]) {
                    const label = `${v.tab}-${v.period}-${method}`;
                    const p = await page(label);
                    const path = `/schedule?tab=${v.tab}&period=${v.period}&date=2026-10-20`;
                    await recover(p, path, method);
                    const target = v.tab === 'dispatch' ? p.locator(`.dispatch-slot[data-user-id="${worker}"][data-hour="11"]`) : v.period === 'month' ? p.locator('.calendar-drop-slot[data-date="2026-10-21"]') : p.locator('.calendar-drop-slot[data-hour="11"]');
                    await p.locator(`[data-job-id="${alpha}"]`).first().dragTo(target);
                    await p.waitForURL(/\/dispatch\?/);
                    const date = v.period === 'month' ? '2026-10-21' : '2026-10-20';
                    const hour = v.period === 'month' ? '09' : '11';
                    assert(await p.getByRole('dialog').isVisible());
                    assert.equal(await p.locator('[name=start]').inputValue(), `${date}T${hour}:00`);
                    assert.equal(await p.locator('[name=end]').inputValue(), `${date}T${v.period === 'month' ? '10' : '12'}:00`);
                    assert(await p.locator(`[name=assignee_ids][value="${worker}"]`).isChecked());
                    await p.screenshot({path: out + '/' + label + '-proposal.png', fullPage: true});
                    record('proposal-only', {label, url: p.url(), start: `${date}T${hour}:00`, noConfirmation: true});
                    // Start again from a genuine error document: period controls
                    // and month keyboard activation must work on recovered DOM.
                    await recover(p, path, method);
                    if (v.period === 'month') {
                        await p.locator('.schedule-month-day[data-date="2026-10-21"]').focus();
                        await p.keyboard.press('Enter');
                        await p.waitForURL(u => u.searchParams.get('period') === 'day' && u.searchParams.get('date') === '2026-10-21');
                        record('month-keyboard', {label, url: p.url()});
                    } else {
                        await p.locator('.schedule-period-tabs a').filter({hasText: 'Week'}).click();
                        await p.waitForURL(u => u.searchParams.get('period') === 'week');
                        assert.equal(await p.locator('.schedule-filters [name=period]').inputValue(), 'week');
                        await p.getByRole('button', {name: 'Next period', exact: true}).click();
                        await p.waitForURL(u => u.searchParams.get('date') === '2026-10-27');
                        record('period-navigation', {label, url: p.url()});
                    }
                    await p.close();
                }
            }
            record('recovery-contract', {calendarDay: true, calendarMonth: true, dispatch: true, refresh: true, visibilityReturn: true, monthKeyboard: true, periodNavigation: true});
        }
        assert.deepEqual(errors, []);
    } finally { await browser.close(); }
})().catch(e => { record('failure', {message: e.stack}); process.exitCode = 1; });
