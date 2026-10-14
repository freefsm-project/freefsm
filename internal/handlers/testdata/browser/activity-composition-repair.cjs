const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');
(async () => {
  const [base, cookie, ...paths] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = await context.newPage();
    for (const path of paths) {
      const kind = path.split('/')[1].slice(0, -1);
      for (const value of ['Original browser observation', 'Revised browser observation']) {
        assert.equal((await page.goto(base + path + '/custom-fields')).status(), 200);
        await page.locator('input[name^="custom_field_"]').fill(value);
        const [saved] = await Promise.all([
          page.waitForResponse(r => r.request().method() === 'POST' && r.url() === base + path + '/custom-fields'),
          page.waitForNavigation(),
          page.getByRole('button', { name: 'Save Custom Fields', exact: true }).click(),
        ]);
        assert.equal(saved.status(), 303);
      }
      await page.goto(base + path + '/custom-fields');
      assert.equal(await page.locator('input[name^="custom_field_"]').inputValue(), 'Revised browser observation');
      for (const history of [path + '/activity', `/activity?object_type=${kind}&object_id=${path.split('/')[2]}`]) {
        await page.goto(base + history);
        assert.match(await page.locator('body').innerText(), /Original browser observation/);
        assert.match(await page.locator('body').innerText(), /Revised browser observation/);
      }
      if (kind !== 'customer') {
        await page.goto(base + path + '/edit');
        await page.locator('[name=title]').fill('Browser revised document');
        await Promise.all([page.waitForURL(new RegExp(path + '\\?')), page.getByRole('button', { name: 'Save', exact: true }).click()]);
        await page.goto(base + path + '/edit');
        assert.equal(await page.locator('[name=title]').inputValue(), 'Browser revised document');
        await page.goto(base + path + '/activity');
        const body = await page.locator('body').innerText();
        assert.match(body, /Browser history document/);
        assert.match(body, /Browser revised document/);
      }
    }
    console.log('PASS persisted separate Estimate/Invoice/Customer field saves and ordinary commercial edits visible in history');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });
