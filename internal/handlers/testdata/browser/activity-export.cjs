const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');
(async () => {
  const [base, cookie] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium', headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({ viewport: { width: 320, height: 844 }, acceptDownloads: true });
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = await context.newPage();
	page.on('response', async response => { if (response.status() >= 400) console.error('HTTP failure', response.status(), response.url(), await response.text().catch(() => '')); });
    await page.goto(base + '/activity?from=2026-02-01&until=2026-03-01&action=updated');
    assert.equal(await page.locator('.activity-item').count(), 25);
    await page.getByRole('link', { name: 'Export activity CSV', exact: true }).click();
    assert.match(await page.locator('body').innerText(), /All matching activity/);
    assert.match(await page.locator('body').innerText(), /All Records/);
    assert.match(await page.locator('body').innerText(), /2026-02-01/);
    await Promise.all([page.waitForURL(/\/exports\/\d+$/), page.getByRole('button', { name: 'Confirm activity export', exact: true }).click()]);
    const result = page.url();
    await page.goto(base + '/activity');
    await page.getByRole('link', { name: 'My CSV exports', exact: true }).click();
    await page.locator(`a[href="${new URL(result).pathname}"]`).click();
    for (let i = 0; i < 30 && !(await page.getByRole('link', { name: 'Download CSV', exact: true }).count()); i++) { await page.waitForTimeout(200); await page.reload(); }
    assert.match(await page.locator('body').innerText(), /Completed: 35 rows/);
    const [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('link', { name: 'Download CSV', exact: true }).click()]);
    assert.equal(await download.failure(), null);
    const stream = await download.createReadStream(); let csv = ''; for await (const chunk of stream) csv += chunk;
    assert.match(csv, /^activity_id,accepted_at,occurred_at,/);
    assert.equal(csv.trim().split('\n').length, 36);
    assert.match(csv, /Browser activity export/);
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth));
    if (process.env.ACTIVITY_EXPORT_SCREENSHOT) await page.screenshot({ path: process.env.ACTIVITY_EXPORT_SCREENSHOT, fullPage: true });
    console.log('Persisted 320px activity export: confirmed filters/scope, all 35 rows, navigation recovery, Ready status, authenticated CSV download.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });
