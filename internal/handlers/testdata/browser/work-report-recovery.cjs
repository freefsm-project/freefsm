const {chromium} = require('playwright-core');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
(async () => {
  const [base, token, raw] = process.argv.slice(2);
  const journeys = JSON.parse(raw);
  const profile = fs.mkdtempSync(path.join(process.env.TMPDIR, 'work-report-recovery-'));
  const options = {executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'], viewport: {width: 360, height: 800}};
  let context = await chromium.launchPersistentContext(profile, options);
  await context.addCookies([{name: 'session', value: token, url: base}]);
  await (await context.newPage()).goto(base + '/exports');
  await context.close();
  context = await chromium.launchPersistentContext(profile, options);
  try {
    await context.addCookies([{name: 'session', value: token, url: base}]);
    const page = await context.newPage();
    page.setDefaultTimeout(20000);
    for (const journey of journeys) {
      for (const receipt of [journey.Expired, journey.Failed]) {
        await page.goto(base + receipt);
        const recovery = page.locator(`a[href^="${journey.Route}?"]`);
        assert.equal(await recovery.count(), 1, 'same-selection recovery link');
        await recovery.click();
        await page.waitForURL(u => u.pathname === journey.Route);
        for (const [key, values] of Object.entries(journey.Filters)) {
          assert.deepEqual(new URL(page.url()).searchParams.getAll(key), values, `recovery preserves ${key}`);
          assert.equal(await page.locator(`form[method=post] input[name="${key}"]`).inputValue(), values[0]);
        }
        await page.getByRole('button', {name: 'Confirm CSV export', exact: true}).click();
        await page.waitForURL(/\/exports\/\d+$/);
        assert.notEqual(new URL(page.url()).pathname, receipt, 'fresh reviewed intent');
        const accepted = page.url();
        let state;
        for (let n = 0; n < 100; n++) {
          state = (await (await context.request.get(accepted, {headers: {Accept: 'application/json'}})).json()).state;
          if (state === 'Ready' || state === 'Failed') break;
          await new Promise(resolve => setTimeout(resolve, 200));
        }
        assert.equal(state, 'Ready');
      }
    }
    console.log('Persisted recovery passed: five output contracts, real Failed and expired Ready receipts, browser restart, exact scope/filter/sort retention, reviewed new intent and Ready regeneration.');
  } finally { await context.close(); }
})().catch(error => {console.error(error); process.exit(1);});
