const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');

(async () => {
  const [base, cookie, adminCookie, role] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium-browser', headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext();
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = await context.newPage();
    assert.equal((await page.goto(base + '/customers/new')).status(), 200);
    assert.equal(await page.locator('[name=billing_address_1]').count(), 0);
    await page.locator('[name=display_name]').fill('Browser persisted create');
    await page.locator('[name=service_notes]').fill('Browser service notes');
    await Promise.all([page.waitForURL(/\/customers\/\d+\/edit$/), page.getByRole('button', { name: 'Save and continue', exact: true }).click()]);
    await page.locator('[name=display_name]').fill('Browser persisted edit');
    await Promise.all([page.waitForURL(/\/customers\/\d+$/), page.getByRole('button', { name: 'Save', exact: true }).click()]);
    assert.match(await page.locator('body').innerText(), /Browser service notes/);
    assert.doesNotMatch(await page.locator('body').innerText(), /Financial Summary|Customer Credit/);
    const customerURL = page.url();

    const administrator = await browser.newContext();
    await administrator.addCookies([{ name: 'session', value: adminCookie, url: base, httpOnly: true }]);
    const control = await administrator.newPage();
    assert.equal((await control.goto(base + '/settings/authorization')).status(), 200);
    const form = control.locator(`form[action="/settings/authorization/roles"]:has(input[name=role_id][value="${role}"])`);
    await form.locator('summary').click();
    while (await form.locator('[name=grant]:checked').count()) await form.locator('[name=grant]:checked').first().uncheck();
    await Promise.all([control.waitForNavigation(), form.getByRole('button', { name: 'Save Role and grants', exact: true }).click()]);
    assert.equal((await page.goto(customerURL)).status(), 404);
    assert.doesNotMatch(await page.locator('body').innerText(), /Browser persisted|Browser service notes/);
    console.log('Production HTTP + CSRF + PostgreSQL: create/edit persisted, restricted financial inputs absent, Administrator grant revocation hides existing Customer.');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
