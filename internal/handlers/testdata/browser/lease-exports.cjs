const {chromium} = require('playwright-core');
const assert = require('node:assert/strict');
const path = require('node:path');

(async () => {
  const fixture = JSON.parse(process.argv[2]);
  const browser = await chromium.launch({executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox']});
  const context = await browser.newContext({acceptDownloads: true, viewport: {width: 320, height: 850}});
  const page = await context.newPage();
  page.setDefaultTimeout(20000);
  async function login(token) {
    await context.clearCookies();
    await context.addCookies([{name: 'session', value: token, url: fixture.base}]);
  }
  async function readyAndDownload(receipt, filename) {
    await page.goto(fixture.base + '/leases/report');
    await page.goto(fixture.base + '/exports');
    await page.locator(`a[href="${new URL(receipt).pathname}"]`).click();
    await page.waitForURL(receipt);
    let ready = false;
    for (let i = 0; i < 100; i++) {
      const response = await context.request.get(receipt, {headers: {Accept: 'application/json'}});
      assert.equal(response.status(), 200);
      const status = await response.json();
      assert.notEqual(status.state, 'Failed', JSON.stringify(status));
      if (status.state === 'Ready') { ready = true; break; }
      await page.waitForTimeout(100);
    }
    assert.ok(ready, 'real persisted worker must reach Ready');
    await page.reload();
    const downloading = page.waitForEvent('download');
    await page.getByRole('link', {name: /Download CSV/}).click();
    const download = await downloading;
    assert.equal(await download.failure(), null);
    await download.saveAs(path.join(fixture.output, filename + '.csv'));
  }
  try {
    let operationalReceipt;
    for (const mode of ['operational', 'billing']) {
      await login(mode === 'operational' ? fixture.operational : fixture.admin);
      await page.goto(fixture.base + '/leases/report?search=Browser+cohort&sort=number_desc&page=2&per_page=25');
      const link = page.locator(`a[href^="/leases/report/export?"]`).filter({hasText: mode === 'operational' ? /Operational/i : /Billing/i});
      assert.equal(await link.count(), 1, mode + ' report export affordance');
      await link.click();
      await page.getByRole('heading', {name: 'Review Lease CSV export', exact: true}).waitFor();
      const review = new URL(page.url());
      assert.equal(review.searchParams.get('page'), null);
      assert.equal(review.searchParams.get('per_page'), null);
      assert.equal(review.searchParams.get('search'), 'Browser cohort');
      assert.equal(review.searchParams.get('sort'), 'number_desc');
      assert.match(await page.locator('body').innerText(), /All Records/);
      assert.match(await page.locator('body').innerText(), /Every matching Lease/);
      if (mode === 'operational') assert.match(await page.locator('body').innerText(), /No Billing columns/);
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'phone review reflows');
      await page.getByRole('link', {name: 'Cancel and return to report', exact: true}).click();
      await page.waitForURL(url => url.pathname === '/leases/report');
      await page.getByRole('heading', {name: 'Lease position report', exact: true}).waitFor();
      assert.equal(new URL(page.url()).searchParams.get('search'), 'Browser cohort');
      await page.goBack();
      await page.getByRole('heading', {name: 'Review Lease CSV export', exact: true}).waitFor();
      const operation = await page.locator('input[name="operation_id"]').inputValue();
      assert.ok(operation);
      await page.getByRole('button', {name: 'Confirm Lease CSV export', exact: true}).focus();
      await page.keyboard.press('Enter');
      await page.waitForURL(/\/exports\/\d+$/);
      const receipt = page.url();
      if (mode === 'operational') operationalReceipt = receipt;
      await readyAndDownload(receipt, mode);
    }
    await login(fixture.admin);
    const bad = await page.goto(fixture.base + '/leases/report/export?mode=billing&as_of=bad&search=Browser+cohort&sort=number_desc');
    assert.equal(bad.status(), 422);
    await page.waitForFunction(() => document.activeElement?.hasAttribute('data-error-summary'));
    assert.equal(await page.locator('input[name="search"]').inputValue(), 'Browser cohort');
    await page.locator('[data-error-summary] a').click();
    await page.locator('input[name="as_of"]').fill('');
    await page.getByRole('button', {name: 'Review corrected export', exact: true}).click();
    await page.getByRole('heading', {name: 'Review Lease CSV export', exact: true}).waitFor();
    assert.equal(await page.locator('input[name="sort"]').inputValue(), 'number_desc');
    await page.goto(fixture.base + fixture.expired);
    assert.match(await page.locator('body').innerText(), /This result is unavailable/);
    const expiredStatus = await context.request.get(fixture.base + fixture.expired, {headers: {Accept: 'application/json'}});
    assert.equal((await expiredStatus.json()).expired, true);
    await page.locator('a[href^="/leases/report/export?"]').click();
    await page.getByRole('heading', {name: 'Review Lease CSV export', exact: true}).waitFor();
    assert.equal(await page.locator('input[name="search"]').inputValue(), 'Browser cohort');
    assert.equal(await page.locator('input[name="sort"]').inputValue(), 'number_desc');
    await page.getByRole('button', {name: 'Confirm Lease CSV export', exact: true}).click();
    await page.waitForURL(/\/exports\/\d+$/);
    assert.notEqual(new URL(page.url()).pathname, fixture.expired, 'regeneration gets new receipt');
    await readyAndDownload(page.url(), 'regenerated');
    await login(fixture.denied);
    await page.goto(fixture.base + fixture.deniedPath);
    assert.doesNotMatch(await page.locator('body').innerText(), /PRIVATE RECOVERY FILTER/);
    assert.equal(await page.locator('a[href^="/leases/report/export?"]').count(), 0);
    await login(fixture.reader);
    await page.goto(fixture.base + '/leases/report?reset=1');
    assert.equal(await page.locator('a[href^="/leases/report/export?"]').count(), 0);
    assert.equal((await context.request.get(operationalReceipt + '/download')).status(), 404);
    assert.equal((await page.goto(fixture.base + '/leases/report/export?mode=operational')).status(), 403);
    console.log('Lease exports: real CSRF forms, both modes, page-independent review, durable navigation, Chromium files, keyboard/error recovery, expiry regeneration, lost-selection privacy and requester isolation');
  } finally {
    await context.close();
    await browser.close();
  }
})().catch(error => { console.error(error); process.exit(1); });
