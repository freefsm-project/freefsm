const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');

(async () => {
  const [base, cookie] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium', headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({ viewport: { width: 320, height: 844 } });
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = await context.newPage();
    await page.goto(base + '/activity');
    assert.equal(await page.locator('.activity-item').count(), 2);
    await page.getByLabel('From (company date, inclusive)').fill('2026-02-01');
    await page.getByLabel('Until (company date, exclusive)').fill('2026-03-01');
    await Promise.all([page.waitForURL(/from=2026-02-01/), page.getByRole('button', { name: 'Refresh activity', exact: true }).click()]);
    assert.equal(await page.locator('.activity-item').count(), 1);
    assert.match(await page.locator('.activity-item').innerText(), /updated.*Browser activity subject/s);
    assert.match(await page.locator('body').innerText(), /Updated at/);
    await page.reload();
    assert.equal(await page.getByLabel('From (company date, inclusive)').inputValue(), '2026-02-01');
    assert.equal(await page.locator('.activity-item').count(), 1);
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth));
    if (process.env.ACTIVITY_SCREENSHOT) await page.screenshot({ path: process.env.ACTIVITY_SCREENSHOT, fullPage: true });
    console.log('Persisted 320px Chromium: company-date filtering, current result timestamp, refresh and reload retain filters, no horizontal overflow.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });
