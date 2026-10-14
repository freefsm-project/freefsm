const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');
(async () => {
  const [base, cookie, job] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({ viewport: { width: 320, height: 740 } });
    context.setDefaultTimeout(15000);
    context.setDefaultNavigationTimeout(15000);
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const first = await context.newPage(), second = await context.newPage();
    const path = `${base}/jobs/${job}/custom-fields`;
    for (const page of [first, second]) assert.equal((await page.goto(path)).status(), 200);
    const submit = async (page, label, status) => {
      console.log(`Submitting ${label}, expecting ${status}`);
      const result = page.waitForResponse(r => r.request().method() === 'POST' && r.url() === path);
      if (status === 303) {
        await Promise.all([
          page.waitForNavigation({ waitUntil: 'domcontentloaded' }),
          page.getByRole('button', { name: label, exact: true }).click(),
        ]);
      } else {
        await page.getByRole('button', { name: label, exact: true }).click();
      }
      const response = await result;
      assert.equal(response.status(), status, response.status() === 303 ? 'redirect' : await response.text());
      console.log(`Submitted ${label}: ${response.status()}`);
    };
    await first.locator('[name=custom_field_1]').fill('First device');
    await second.locator('[name=custom_field_2]').fill('Second device');
    await submit(first, 'Save Custom Fields', 303);
    await submit(second, 'Save Custom Fields', 303);
    console.log('Disjoint saves complete');
    for (const page of [first, second]) {
      await page.goto(path);
      assert.equal(await page.locator('[name=custom_field_1]').inputValue(), 'First device');
      assert.equal(await page.locator('[name=custom_field_2]').inputValue(), 'Second device');
    }
    const rejectedID = await second.locator('[name=field_operation_id]').inputValue();
    await first.locator('[name=custom_field_1]').fill('Concurrent accepted value');
    await submit(first, 'Save Custom Fields', 303);
    await second.locator('[name=custom_field_1]').fill('Retained proposed value');
    await submit(second, 'Save Custom Fields', 409);
    await second.getByRole('heading', { name: 'Current saved values' }).waitFor();
    assert.match(await second.locator('body').innerText(), /Concurrent accepted value/);
    assert.equal(await second.locator('[name=custom_field_1]').inputValue(), 'Retained proposed value');
    assert.notEqual(await second.locator('[name=field_operation_id]').inputValue(), rejectedID);
    await submit(second, 'Review and save as new operation', 303);
    await first.goto(path);
    assert.equal(await first.locator('[name=custom_field_1]').inputValue(), 'Retained proposed value');
    assert.equal(await first.locator('[name=custom_field_2]').inputValue(), 'Second device');
    assert.equal((await first.goto(`${base}/jobs/${job}/edit`)).status(), 403);
    assert.equal((await first.goto(`${base}/settings/custom-fields`)).status(), 403);
    await first.goto(path);
    assert.equal(await first.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    console.log('Persisted 320px Chromium: independent assigned authority, disjoint saves, stale comparison, retained intent and explicit new-ID repair; planning/definition denials.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
