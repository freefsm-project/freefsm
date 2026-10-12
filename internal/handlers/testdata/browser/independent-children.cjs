const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');
(async () => {
  const [base, cookie, customer] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext();
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = await context.newPage();
    await page.goto(base + '/preferences');
    for (const [kind, label, name] of [['contact', 'Contacts', 'Browser person'], ['location', 'Locations', 'Browser place']]) {
      await page.getByRole('link', { name: label, exact: true }).click();
      await Promise.all([page.waitForURL(new RegExp(`/${kind}s/\\d+$`)), page.getByRole('link', { name, exact: true }).click()]);
      assert.match(new URL(page.url()).pathname, new RegExp(`^/${kind}s/\\d+$`));
      assert.doesNotMatch(await page.locator('body').innerText(), /SECRET_|Back to Customer/);
      assert.equal(await page.locator('a[href^="/customers"]').count(), 0);
      await page.getByRole('link', { name: `Edit ${kind[0].toUpperCase() + kind.slice(1)}`, exact: true }).click();
      await page.getByLabel('Service Notes', { exact: true }).fill('Independent persisted ' + kind);
      await Promise.all([page.waitForNavigation(), page.getByRole('button', { name: 'Save', exact: true }).click()]);
      await page.reload();
      assert.match(await page.locator('body').innerText(), new RegExp('Independent persisted ' + kind));
      const response = await page.request.get(page.url(), { headers: { Accept: 'application/json' } });
      assert.equal(response.status(), 200);
      assert.equal(Object.hasOwn(await response.json(), 'customer_id'), false);
      await Promise.all([page.waitForNavigation(), page.getByRole('button', { name: 'Archive', exact: true }).click()]);
      assert.match(await page.locator('body').innerText(), /Archived/);
      await Promise.all([page.waitForNavigation(), page.getByRole('button', { name: 'Restore', exact: true }).click()]);
      assert.doesNotMatch(await page.locator('body').innerText(), /SECRET_|Back to Customer/);
    }
    assert.equal((await page.goto(base + '/customers/' + customer)).status(), 404);
    console.log('Independent Contact/Location navigation, read/edit, persisted reload, archive/restore, redacted JSON and denied Customer master: passed.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
