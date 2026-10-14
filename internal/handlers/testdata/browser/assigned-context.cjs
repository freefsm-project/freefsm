const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');

(async () => {
  const [base, cookie, adminCookie, role, job] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({ viewport: { width: 320, height: 740 } });
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = await context.newPage();
    const path = `${base}/jobs/${job}/customer-context`;
    assert.equal((await page.goto(path)).status(), 200);
    assert.match(await page.locator('body').innerText(), /Browser operational instructions/);
    assert.match(await page.locator('body').innerText(), /41.25, -87.75/);
    assert.doesNotMatch(await page.locator('body').innerText(), /PRIVATE-|Lease|Credit|Billing/);
    assert.match(await page.getByRole('link', { name: 'Navigate to service Location' }).getAttribute('href'), /41.25%2C-87.75/);
    if (process.env.ASSIGNED_CONTEXT_EVIDENCE) await page.screenshot({ path: `${process.env.ASSIGNED_CONTEXT_EVIDENCE}/context-phone.png`, fullPage: true });
    const administrator = await browser.newContext();
    await administrator.addCookies([{ name: 'session', value: adminCookie, url: base, httpOnly: true }]);
    const control = await administrator.newPage();
    assert.equal((await control.goto(base + '/settings/authorization')).status(), 200);
    const form = control.locator(`form[action="/settings/authorization/roles"]:has(input[name=role_id][value="${role}"])`);
    await form.locator('summary').click();
    while (await form.locator('[name=grant]:checked').count()) await form.locator('[name=grant]:checked').first().uncheck();
    await Promise.all([control.waitForNavigation(), form.getByRole('button', { name: 'Save Role and grants', exact: true }).click()]);
    assert.equal((await page.goto(path)).status(), 404);
    assert.doesNotMatch(await page.locator('body').innerText(), /Browser operational|41.25|Visible browser/);
    console.log('Persisted phone context, coordinates/navigation, negative disclosure, and Administrator revocation passed.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
