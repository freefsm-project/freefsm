const {chromium} = require('playwright-core');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

(async () => {
  const [base, token, reader] = process.argv.slice(2);
  const profile = fs.mkdtempSync(path.join(process.env.TMPDIR, 'financial-reports-'));
  const options = {executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'], viewport: {width: 360, height: 800}, acceptDownloads: true};
  let context = await chromium.launchPersistentContext(profile, options);
  const selections = [['invoice-aging', 'invoices', 'invoice-aging'], ['invoice-aging', 'customers', 'customer-financials'], ['payments-credits', 'settlements', 'settlements'], ['payments-credits', 'credits', 'credits'], ['payments-credits', 'combined', 'payments-credits']];
  try {
    await context.addCookies([{name: 'session', value: token, url: base}]);
    let page = await context.newPage();
    page.setDefaultTimeout(20000);
    const artifacts = [];
    for (const [route, view, exportRoute] of selections) {
      await page.goto(`${base}/reports/${route}?view=${view}`);
      assert.match(await page.locator('body').innerText(), /Browser financial source/);
      if (view === 'combined') {
        const creditSection = page.locator('section').filter({has: page.getByRole('heading', {name: 'Current Available Credit', exact: true})});
        const before = await creditSection.innerText();
        await page.locator('[name=from]').fill('2020-01-01');
        await page.locator('[name=to]').fill('2020-01-01');
        await page.getByRole('button', {name: 'Apply filters / Refresh', exact: true}).click();
        assert.equal(await creditSection.innerText(), before, 'changing period changed current Credit');
        assert.match(await page.locator('body').innerText(), /Unknown recording-time history/);
      }
      const review = page.getByRole('button', {name: /^Review .* CSV export$/});
      await review.focus();
      await page.keyboard.press('Enter');
      await page.waitForURL(u => u.pathname === `/reports/${exportRoute}/export`);
      await page.getByRole('button', {name: 'Confirm CSV export', exact: true}).click();
      await page.waitForURL(/\/exports\/\d+$/);
      artifacts.push([page.url(), view]);
    }
    await context.close();
    context = await chromium.launchPersistentContext(profile, options);
    await context.addCookies([{name: 'session', value: token, url: base}]);
    page = await context.newPage();
    page.setDefaultTimeout(20000);
    for (const [receipt, view] of artifacts) {
      await page.goto(base + '/exports');
      await page.locator(`a[href="${new URL(receipt).pathname}"]`).click();
      await page.waitForURL(receipt);
      let ready = false;
      for (let attempt = 0; attempt < 100; attempt++) {
        const response = await context.request.get(receipt, {headers: {Accept: 'application/json'}});
        assert.equal(response.status(), 200);
        const status = await response.json();
        assert.notEqual(status.state, 'Failed');
        if (status.state === 'Ready') { ready = true; break; }
        await page.waitForTimeout(200);
      }
      assert.ok(ready, `${view} worker did not reach Ready`);
      await page.reload();
      console.log(`Downloading ${view} from ${receipt}; page=${page.url()}`);
      let download;
      try {
        [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('link', {name: /Download CSV/}).click()]);
      } catch (error) {
        console.error(`Download failure ${view}: ${page.url()}\n${await page.locator('body').innerText()}`);
        throw error;
      }
      const bytes = fs.readFileSync(await download.path(), 'utf8');
      assert.match(bytes, /^as_of,timezone,row_type,/);
      assert.match(bytes, /Browser financial source/);
      if (view === 'invoices' || view === 'customers') assert.match(bytes, /50\.00/);
      if (view === 'credits' || view === 'combined') assert.match(bytes, /current_credit_total/);
      if (view === 'settlements') assert.doesNotMatch(bytes, /current_available_amount/);
      if (view === 'credits') assert.doesNotMatch(bytes, /event_type|cash_effect|method|reference|notes/);
    }
    await context.clearCookies();
    await context.addCookies([{name: 'session', value: reader, url: base}]);
    for (const [route, view, exportRoute] of selections) {
      await page.goto(`${base}/reports/${route}?view=${view}`);
      assert.equal(await page.getByRole('button', {name: /^Review .* CSV export$/}).count(), 0);
      assert.equal((await context.request.get(`${base}/reports/${exportRoute}/export`)).status(), 403);
    }
    assert.equal((await context.request.get(artifacts[0][0] + '/download')).status(), 404);
    console.log('Five financial exports: reviewed/confirmed, durable receipts after browser restart, actual CSV downloads, independent period/current Credit, keyboard review, narrow viewport, denied controls/direct reviews and requester isolation.');
  } finally { await context.close(); fs.rmSync(profile, {recursive: true, force: true}); }
})().catch(error => { console.error(error); process.exit(1); });
