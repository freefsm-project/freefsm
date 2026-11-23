const {chromium} = require('playwright-core');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

(async () => {
  const [base, token, reader] = process.argv.slice(2);
  const profile = fs.mkdtempSync(path.join(process.env.TMPDIR, 'work-reports-'));
  const options = {executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'], viewport: {width: 360, height: 800}, acceptDownloads: true};
  let context = await chromium.launchPersistentContext(profile, options);
  try {
    await context.addCookies([{name: 'session', value: token, url: base}]);
    let page = await context.newPage();
    page.setDefaultTimeout(20000);
    const artifacts = [];
    for (const [route, fact] of [['workload', 'Browser workload fact'], ['estimate-cohort', 'Browser cohort fact']]) {
      await page.goto(`${base}/reports/${route}?scope=A`);
      assert.match(await page.locator('tbody').innerText(), new RegExp(fact));
      const sourceHref = await page.locator('tbody th a').first().getAttribute('href');
      const sourceID = sourceHref.split('/').pop();
      if (route === 'workload') {
        await page.locator('[name=measure]').selectOption('unscheduled');
        await page.getByRole('button', {name: 'Apply filters / Refresh', exact: true}).click();
        assert.equal(new URL(page.url()).searchParams.get('measure'), 'unscheduled');
        assert.match(await page.locator('body').innerText(), /nonadditive/);
      } else {
        assert.match(await page.locator('body').innerText(), /Current state.*creation cohort/);
      }
      await page.getByRole('button', {name: 'Review CSV export', exact: true}).click();
      await page.waitForURL(u => u.pathname === `/reports/${route}/export`);
      if (route === 'workload') assert.match(await page.locator('body').innerText(), /unscheduled/);
      await page.getByRole('button', {name: 'Confirm CSV export', exact: true}).click();
      await page.waitForURL(/\/exports\/\d+$/);
      artifacts.push([page.url(), fact, route, sourceID]);
    }
    // Close the entire browser, retaining only its on-disk profile. Server-side
    // export intent and authentication must survive a real navigation restart.
    await context.close();
    context = await chromium.launchPersistentContext(profile, options);
    // Session cookies are intentionally session-only, so restore authentication
    // explicitly; export identifiers and bytes are recovered from persisted DB.
    await context.addCookies([{name: 'session', value: token, url: base}]);
    page = await context.newPage();
    page.setDefaultTimeout(20000);
    for (const [accepted, fact, route, sourceID] of artifacts) {
      await page.goto(base + '/exports');
      await page.locator(`a[href="${new URL(accepted).pathname}"]`).click();
      for (let attempt = 0; attempt < 100; attempt++) {
        const result = await context.request.get(accepted, {headers: {Accept: 'application/json'}});
        const status = await result.json();
        assert.notEqual(status.state, 'Failed');
        if (status.state === 'Ready') break;
        await new Promise(resolve => setTimeout(resolve, 200));
      }
      await page.reload();
      const downloading = page.waitForEvent('download');
      await page.getByRole('link', {name: /Download CSV/}).click();
      const bytes = fs.readFileSync(await (await downloading).path(), 'utf8');
      if (route === 'workload') assert.match(bytes, new RegExp(fact));
      else {
        const lines = bytes.trim().split('\n').map(line => line.trim().split(','));
        assert.equal(lines.length, 2);
        assert.equal(lines[1][lines[0].indexOf('estimate_id')], sourceID);
      }
      assert.doesNotMatch(bytes, /unit_price|grand_total|latitude|longitude/);
    }
    await context.clearCookies();
    await context.addCookies([{name: 'session', value: reader, url: base}]);
    for (const route of ['workload', 'estimate-cohort']) {
      await page.goto(`${base}/reports/${route}?scope=A`);
      assert.equal(await page.getByRole('button', {name: 'Review CSV export', exact: true}).count(), 0);
    }
    assert.equal((await context.request.get(artifacts[0][0]+'/download')).status(), 404);
    console.log('Persisted report browser passed: filtered workload/cohort, honest current-state/nonadditive labels, reviewed exports, browser restart, durable history recovery, CSV bytes, reader denial and requester isolation.');
  } finally { await context.close(); }
})().catch(error => { console.error(error); process.exit(1); });
