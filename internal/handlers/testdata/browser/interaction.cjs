const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');

(async () => {
  const [base, cookie] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium', headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({ viewport: { width: 320, height: 640 }, hasTouch: true, reducedMotion: 'reduce', colorScheme: 'dark' });
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = await context.newPage();
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
    await page.waitForURL(/\/customers$/);
    assert.match(await page.locator('.page-body').innerText(), /Accessible persisted customer/);
    assert.doesNotMatch(await page.locator('.page-body').innerText(), /Discard this edit/);
    await page.getByRole('button', { name: 'Edit Accessible persisted customer', exact: true }).click();
    await page.locator('[name=phone]').fill('Unsaved Back input');
    await page.evaluate(() => history.back());
    await leave.getByRole('button', { name: 'Stay', exact: true }).click();
    assert.equal(await page.locator('[name=phone]').inputValue(), 'Unsaved Back input');
    await page.evaluate(() => history.back());
    await leave.getByRole('button', { name: 'Discard changes', exact: true }).click();
    await page.waitForURL(/\/customers$/);
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
    // Boosted navigation updates the URL before HTMX initializes the new form.
    // Wait for this navigation's settle, not the separate activity widget load.
    await page.evaluate(() => {
      window.customerListSettled = new Promise(resolve => {
        const settled = event => {
          if (event.detail.target !== document.body || new URL(event.detail.xhr.responseURL).pathname !== '/customers') return;
          document.removeEventListener('htmx:afterSettle', settled);
          resolve();
        };
        document.addEventListener('htmx:afterSettle', settled);
      });
    });
    await page.getByRole('link', { name: 'Clear filters', exact: true }).click();
    await page.waitForURL(/\/customers$/);
    await page.evaluate(() => window.customerListSettled);
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
    await page.getByText('Results unavailable. Previous results are not current.', { exact: true }).waitFor();
    assert.equal(await persistedRow.count(), 1, 'the previous persisted result is retained');
    assert.equal(await persistedRow.isVisible(), false, 'failed results cannot certify retained rows');
    await page.unroute(failedSearchURL);
    await page.getByRole('button', { name: 'Retry results', exact: true }).click();
    await page.waitForURL(failedSearchURL);
    assert.match(await page.locator('.page-body').innerText(), /Accessible persisted customer/);
    const listURL = base + '/customers?search=Context&sort=name_desc&per_page=25&page=2';
    await page.goto(listURL);
    const row = page.locator('.work-queue-title').nth(12);
    await row.scrollIntoViewIfNeeded();
    const beforeScroll = await page.evaluate(() => scrollY);
    assert.ok(beforeScroll > 100, 'long list can scroll');
    await row.click();
    await page.waitForURL(/\/customers\/\d+$/);
    await page.goBack();
    await page.waitForURL(listURL);
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
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
