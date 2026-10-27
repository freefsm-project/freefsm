const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');
(async () => {
  const [base, cookie, target] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = await context.newPage();
    await page.goto(base + '/assets');
    await page.getByRole('link', { name: 'Archived Assets', exact: true }).click();
    const row = page.locator(`#asset-list-content a[href="${target}"]`);
    await row.waitFor();
    await page.getByRole('searchbox', { name: 'Search', exact: true }).fill('ARCHIVED_ASSET_LIST_63');
    await page.getByRole('searchbox', { name: 'Search', exact: true }).press('Enter');
    await row.waitFor();
    assert.ok(await page.locator('input[name="archived"][value="1"]').count() > 0);
    assert.equal(await page.locator(`#asset-list-content a[href="${target}/edit"], #asset-list-content form[action="${target}/delete"]`).count(), 0);
    await row.click();
    await page.getByText(/Archived on/).waitFor();
    await Promise.all([page.waitForNavigation(), page.getByRole('button', { name: 'Restore', exact: true }).click()]);
    await page.goto(base + target);
    assert.doesNotMatch(await page.locator('body').innerText(), /Archived on/);
    console.log('Persisted authorized Asset archive list, scope-preserving search and Restore passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
