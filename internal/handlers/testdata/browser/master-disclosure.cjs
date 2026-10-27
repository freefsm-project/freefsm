const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');
(async () => {
  const [base, cookie, job, project, customer, ...targets] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = await context.newPage();
    for (const path of [job, project, customer, '/search?q=CLOSED', '/contacts?search=CLOSED', '/locations?search=CLOSED', '/assets?search=CLOSED', '/items?search=CLOSED']) {
      await page.goto(base + path, { waitUntil: 'networkidle' });
      assert.doesNotMatch(await page.locator('body').innerText(), /PRIVATE_(LOCATION_ADDRESS|CONTACT_PHONE|ASSET_NOTES|ITEM_DESCRIPTION|SERVICE_RECORD)/);
      for (const target of targets) {
        assert.equal(await page.locator(`a[href="${target}"], [hx-get^="${target}"]`).count(), 0, `${path} expanded target navigation: ${target}`);
      }
      if (path === job) {
        const asset = page.locator('p').filter({ hasText: 'CLOSED_ASSET' });
        assert.equal(await asset.count(), 1);
        assert.match(await asset.innerText(), /Archived/);
      }
      if (path.includes('?')) assert.doesNotMatch(await page.locator('body').innerText(), /CLOSED_(CONTACT|LOCATION|ASSET|ITEM)/);
    }
    for (const target of targets) {
      const response = await page.goto(base + target);
      assert.ok([403, 404].includes(response.status()), `hidden target became accessible: ${target}`);
    }
    console.log('Persisted master sources, lazy widgets, search, archived badge and target denial passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
