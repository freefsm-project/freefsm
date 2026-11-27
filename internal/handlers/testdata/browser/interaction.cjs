const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');

(async () => {
  const [base, cookie] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium', headless: true, args: ['--no-sandbox'] });
  const artifacts = fs.mkdtempSync(path.join(process.env.E || process.env.TMPDIR || os.tmpdir(), 'interaction-retry-'));
  let context, page;
  const events = [];
  try {
    context = await browser.newContext({ viewport: { width: 320, height: 640 }, hasTouch: true, reducedMotion: 'reduce', colorScheme: 'dark' });
    await context.tracing.start({screenshots:false,snapshots:false});
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    page = await context.newPage();
    page.on('response', response => {
      const request = response.request(), url = new URL(response.url());
      if (url.pathname === '/customers' || url.pathname === '/customers/state') {
        const revision = request.method() === 'POST' ? new URLSearchParams(request.postData() || '').get('revision') : null;
        events.push({event:'response',url:url.pathname+url.search,status:response.status(),method:request.method(),revision,time:Date.now()});
      }
    });
    page.on('requestfailed',request => { const url=new URL(request.url());events.push({event:'failed',url:url.pathname+url.search,error:request.failure()?.errorText,time:Date.now()}); });
    await page.goto(base + '/customers');
    const menu = page.getByRole('button', { name: 'Menu', exact: true });
    await menu.click();
    assert.equal(await menu.getAttribute('aria-expanded'), 'true');
    assert.equal(await page.locator('#sidebar a[href="/customers"]').getAttribute('aria-current'), 'page');
    await page.keyboard.press('Escape');
    assert.equal(await menu.getAttribute('aria-expanded'), 'false');
    assert.equal(await menu.evaluate(el => el === document.activeElement), true);
    await page.getByRole('button', { name: 'New Customer', exact: true }).click();
    await page.locator('[name=display_name]').fill('Accessible persisted customer');
    assert.match(await page.locator('[data-form-status]').innerText(), /Unsaved/);
    await page.getByRole('button', { name: 'Cancel', exact: true }).click();
    const leave = page.getByRole('dialog', { name: 'Unsaved changes' });
    await leave.getByRole('button', { name: 'Stay', exact: true }).click();
    assert.equal(await page.locator('[name=display_name]').inputValue(), 'Accessible persisted customer');
    await page.getByRole('button', { name: 'Save and continue', exact: true }).click();
    await page.waitForURL(/\/customers\/\d+\/edit$/);
    await page.reload();
    assert.equal(await page.locator('[name=display_name]').inputValue(), 'Accessible persisted customer');
    for (const size of [{ width: 320, height: 640 }, { width: 640, height: 320 }]) {
      await page.setViewportSize(size);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, 'form reflows');
      assert.equal(await page.getByRole('button', { name: 'Save', exact: true }).isVisible(), true);
    }
    await page.locator('[name=display_name]').fill('Discard this edit');
    await page.getByRole('button', { name: 'Cancel', exact: true }).click();
    await leave.getByRole('button', { name: 'Discard changes', exact: true }).click();
    await page.waitForURL(/\/customers(?:\?.*)?$/);
    assert.match(await page.locator('.page-body').innerText(), /Accessible persisted customer/);
    assert.doesNotMatch(await page.locator('.page-body').innerText(), /Discard this edit/);
    await page.getByRole('button', { name: 'Edit Accessible persisted customer', exact: true }).click();
    await page.locator('[name=phone]').fill('Unsaved Back input');
    await page.evaluate(() => history.back());
    await leave.getByRole('button', { name: 'Stay', exact: true }).click();
    assert.equal(await page.locator('[name=phone]').inputValue(), 'Unsaved Back input');
    await page.evaluate(() => history.back());
    await leave.getByRole('button', { name: 'Discard changes', exact: true }).click();
    await page.waitForURL(/\/customers(?:\?.*)?$/);
    await page.getByRole('button', { name: 'Edit Accessible persisted customer', exact: true }).click();
    await page.locator('[name=display_name]').fill('');
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await page.locator('[data-error-summary]').waitFor();
    assert.equal(await page.locator('[data-error-summary]').evaluate(el => el === document.activeElement), true);
    await page.locator('[data-error-summary] a').first().click();
    assert.equal(await page.locator('[name=display_name]').evaluate(el => el === document.activeElement), true);
    await page.locator('[name=display_name]').fill('Accessible persisted customer');
    await page.locator('[name=phone]').fill('Lost response persisted');
    let lost = false;
    await page.route('**/customers/*', async route => {
      if (!lost && route.request().method() === 'POST') {
        lost = true;
        // Test transport closes only after production routing commits a 303.
        await route.continue({ headers: { ...route.request().headers(), 'X-Test-Lose-Response': 'true' } });
      } else await route.continue();
    });
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await page.waitForFunction(() => !document.querySelector('input[name=display_name]'), { timeout: 10000 }).catch(async error => { console.log(await page.locator('body').innerText()); throw error; });
    assert.match(await page.locator('.page-body').innerText(), /Lost response persisted/);
    await page.unroute('**/customers/*');
    await page.goto(base + '/customers?search=does-not-match&sort=name_desc&per_page=50');
    await page.getByText('No matches for these filters.', { exact: true }).waitFor();
    // Recovery is a normal navigation and preserves presentation and scope.
    await page.getByRole('link', { name: 'Clear filters', exact: true }).click();
    await page.waitForURL(url => url.pathname === '/customers' && url.searchParams.get('q') === '' && url.searchParams.get('sort') === 'name_desc' && url.searchParams.get('per_page') === '50');
    await page.locator('.work-queue-title').filter({hasText:'Context row 080'}).waitFor();
    // Put the known persisted record on page one for the independent transport
    // failure scenario; clearing filters intentionally does not reset sorting.
    await page.goto(base+'/customers?search=&sort=name&per_page=25');
    const persistedRow = page.locator('#customer-list-content .work-queue-title').filter({ hasText: 'Accessible persisted customer' });
    await persistedRow.waitFor();
    const failedSearchURL = base + '/customers?per_page=25&sort=name&search=Accessible';
    await page.route(failedSearchURL, route => route.fulfill({ status: 503, contentType: 'text/plain', body: 'injected dependency failure' }));
    const failedSearch = page.waitForResponse(response => response.url() === failedSearchURL);
    await page.locator('.focused-list-search [name=search]').fill('Accessible');
    await page.locator('.focused-list-search [name=search]').press('Enter');
    const failedResponse = await failedSearch;
    assert.equal(failedResponse.status(), 503);
    assert.equal(failedResponse.request().method(), 'GET');
    assert.equal(failedResponse.request().headers()['hx-request'], 'true');
    await page.getByText('Results unavailable. Previous results are unavailable until authority can be checked. Your filters are retained.', { exact: true }).waitFor();
    assert.equal(await persistedRow.count(), 1, 'the previous persisted result is retained');
    assert.equal(await persistedRow.isVisible(), false, 'failed results cannot certify retained rows');
    await page.unroute(failedSearchURL);
    await page.getByRole('button', { name: 'Retry results', exact: true }).click();
    await page.waitForURL(url => url.pathname === '/customers' && url.searchParams.get('search') === 'Accessible' && url.searchParams.get('per_page') === '25' && url.searchParams.get('sort') === 'name');
    await page.locator('[data-list-save-status]').getByText('Filters remembered',{exact:true}).waitFor();
    await persistedRow.waitFor({state:'visible'});
    assert.match(await page.locator('.page-body').innerText(), /Accessible persisted customer/);
    const listURL = base + '/customers?search=Context&sort=name_desc&per_page=25&page=2';
    if (process.env.FREEFSM_INTERACTION_STALE_GET === '1') {
      const parser = await context.newPage();
      // Deterministic other-tab interleaving: GET is authorized and rendered at
      // revision R, then a real CSRF-protected preference POST commits R+1 before
      // that GET reaches this tab. Its automatic save must not overwrite R+1.
      await page.route(listURL,async route => {
        const response = await route.fetch();
        const html = await response.text();
        const fields = await parser.evaluate(html => {
          const form = new DOMParser().parseFromString(html,'text/html').querySelector('[data-list-save]');
          return Object.fromEntries(new FormData(form));
        },html);
        const revision = fields.revision;
        const csrf = fields.csrf_token;
        assert.ok(revision !== undefined && csrf,'GET carries revision and CSRF');
        const concurrent = await context.request.post(base+'/customers/state',{
          headers:{'X-CSRF-Token':csrf,'X-List-State':'save','Referer':listURL},
          form:fields,
        });
        assert.equal(concurrent.status(),204,'other-tab presentation commit: '+await concurrent.text());
        events.push({event:'interleaved-preference-commit',revision,status:concurrent.status(),time:Date.now()});
        await route.fulfill({response,body:html});
        await parser.close();
      });
    }
    await page.goto(listURL);
    if (process.env.FREEFSM_INTERACTION_STALE_GET === '1') {
      await page.unroute(listURL);
      await page.getByRole('button',{name:'Retry filters',exact:true}).waitFor();
      assert.equal(await page.locator('.work-queue-title').first().isVisible(),false,'CAS conflict must hide uncertain rows');
      await page.getByRole('button',{name:'Retry filters',exact:true}).click();
    }
    await page.locator('[data-list-save-status]').getByText('Filters remembered',{exact:true}).waitFor();
    const row = page.locator('.work-queue-title').nth(12);
    await row.scrollIntoViewIfNeeded();
    const beforeScroll = await page.evaluate(() => scrollY);
    assert.ok(beforeScroll > 100, 'long list can scroll');
    await row.click();
    await page.waitForURL(/\/customers\/\d+$/);
    await page.goBack();
    await page.waitForURL(url => url.pathname === '/customers' && url.searchParams.get('search') === 'Context' && url.searchParams.get('sort') === 'name_desc' && url.searchParams.get('per_page') === '25' && url.searchParams.get('page') === '2');
    await page.waitForFunction(y => Math.abs(scrollY-y) < 5, beforeScroll, {timeout: 5000}).catch(async error => { console.log('Scroll expected', beforeScroll, await page.evaluate(() => ({y:scrollY,storage:{...sessionStorage},url:location.href,body:document.body.innerText.slice(0,500)}))); throw error; });
    await page.locator('.work-queue-title').first().waitFor({ timeout: 5000 }).catch(async error => { console.log('Back body:', await page.locator('body').innerText()); throw error; });
    assert.match(await page.locator('.work-queue-title').first().innerText(), /Context row 055/);
    await page.goto(base + '/jobs/new');
    const opener = page.locator('button[onclick="openJobCreateModal(\'project\')"]');
    await opener.click();
    await page.locator('#job-project-dialog[open]').waitFor();
    assert.ok(await page.locator('#job-project-dialog').getAttribute('aria-labelledby'), 'dialog has a semantic name');
    await page.keyboard.press('Escape');
    assert.equal(await opener.evaluate(el => el === document.activeElement), true, 'dialog restores focus');
    await page.goto(base + '/preferences');
    await page.getByLabel('Theme', { exact: true }).selectOption('light');
    await page.getByRole('button', { name: 'Save Preferences', exact: true }).click();
    await page.waitForURL(/flash=Preferences/);
    const device = await browser.newContext({ colorScheme: 'dark' });
    await device.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const second = await device.newPage();
    await second.goto(base + '/preferences');
    assert.equal(await second.locator('html').getAttribute('data-theme'), 'light');
    console.log('Chromium', browser.version(), ': persisted Customer creation/reload/lost-response receipt; keyboard drawer/current section and Job dialog focus; link/Back Stay/Discard; linked validation; 320px orientations; retained search retry; filter/sort/page/scroll Back; cross-device theme. Native zoom is separately measured in accessibility.cjs.');
  } catch (error) {
    if (page && !page.isClosed()) {
      const state = await page.evaluate(() => ({
        url:location.pathname+location.search,
        preference:document.querySelector('[data-list-save-status]')?.textContent,
        read:document.querySelector('[data-results-state]')?.textContent,
        rows:[...document.querySelectorAll('.work-queue-title')].slice(0,15).map(row=>({text:row.textContent,hiddenAncestors:[...function*(n){while(n){if(n.hidden)yield n.className;n=n.parentElement;}}(row)]})),
      }));
      fs.writeFileSync(path.join(artifacts,'failure-state.json'),JSON.stringify(state,null,2));
      console.log('Failure state:',JSON.stringify(state));
      await page.screenshot({path:path.join(artifacts,'failure.png'),fullPage:true});
    }
    throw error;
  } finally {
    fs.writeFileSync(path.join(artifacts,'responses.json'),JSON.stringify(events,null,2));
    if (context) await context.tracing.stop({path:path.join(artifacts,'trace.zip')});
    console.log('Interaction artifacts:',artifacts);
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
