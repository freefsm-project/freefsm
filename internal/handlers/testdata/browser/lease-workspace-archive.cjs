const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');

(async () => {
  const [base, cookie, id] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({ viewport: { width: 320, height: 740 } });
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = await context.newPage();
    assert.equal((await page.goto(`${base}/leases/${id}`)).status(), 200);
    await page.getByText('Archived — read-only history', { exact: true }).waitFor();
    const restore = page.getByRole('button', { name: 'Restore Lease', exact: true });
    await restore.focus();
    const response = page.waitForResponse(r => r.request().method() === 'POST' && r.url().endsWith(`/leases/${id}/restore`));
    await page.keyboard.press('Enter');
    assert.ok([200, 303].includes((await response).status()));
    await page.waitForLoadState('networkidle');
    await page.reload();
    assert.equal(await page.getByText('Archived — read-only history', { exact: true }).count(), 0);
    assert.equal(await page.getByRole('button', { name: 'Restore Lease', exact: true }).count(), 0);
    assert.equal(await page.getByRole('button', { name: 'Archive Lease', exact: true }).count(), 0);
    assert.match(await page.content(), /terminated/i);
    console.log('320px keyboard restore persisted; Terminated history remains; independent archive authority absent');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exit(1); });
