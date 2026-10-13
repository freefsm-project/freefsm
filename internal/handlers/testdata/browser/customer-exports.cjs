const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');

(async () => {
  const [base, cookie] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium', headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({ viewport: { width: 390, height: 844 }, acceptDownloads: true });
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = await context.newPage();
    await page.goto(base + '/customers?search=Browser&sort=name_desc&per_page=25');
    await page.getByRole('link', { name: 'Export Customer CSV', exact: true }).click();
    await page.getByRole('heading', { name: 'Export Customer operations', exact: true }).waitFor();
    assert.match(await page.locator('body').innerText(), /Search: Browser.*Sorting: name_desc/);
    assert.match(await page.locator('body').innerText(), /All matching rows/);
    await Promise.all([page.waitForURL(/\/exports\/\d+$/), page.getByRole('button', { name: 'Confirm export', exact: true }).click()]);
    const result = page.url();
    await page.goto(base + '/customers');
    await page.getByRole('link', { name: 'My CSV exports', exact: true }).click();
    await page.getByRole('heading', { name: 'My CSV exports', exact: true }).waitFor();
    await page.locator(`a[href="${new URL(result).pathname}"]`).click();
    await page.getByRole('heading', { name: 'CSV export', exact: true }).waitFor();
    for (let i = 0; i < 20 && !(await page.getByRole('link', { name: 'Download CSV', exact: true }).count()); i++) {
      await page.waitForTimeout(250);
      await page.reload();
    }
    assert.match(await page.locator('body').innerText(), /Completed: 1 rows/);
    assert.match(await page.locator('body').innerText(), /Snapshot as of/);
    assert.match(await page.locator('body').innerText(), /outside subsequent access control/);
    const [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('link', { name: 'Download CSV', exact: true }).click()]);
    assert.equal(await download.failure(), null);
    const stream = await download.createReadStream();
    let csv = ''; for await (const chunk of stream) csv += chunk;
    assert.match(csv, /Browser CSV Customer/);
    assert.doesNotMatch(csv, /billing|credit|settlement/);
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth));
    console.log('Persisted mobile Chromium: filtered confirmation, durable navigation recovery, Ready snapshot/count, authenticated CSV transfer and copy warning.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });
