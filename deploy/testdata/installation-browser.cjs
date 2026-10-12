// Real executable + PostgreSQL; no intercepted responses or seeded sessions.
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium-browser', headless: true });
  try {
    const page = await browser.newPage();
    const base = process.argv[2];
    await page.goto(`${base}/login`);
    await page.locator('[name=email]').fill('admin@example.test');
    await page.locator('[name=password]').fill('Temporary-107!');
    await Promise.all([page.waitForURL('**/change-password'), page.locator('form[action="/login"] button[type=submit]').click()]);
    await page.goto(`${base}/customers`);
    assert.equal(new URL(page.url()).pathname, '/change-password');
    await page.locator('[name=current_password]').fill('Temporary-107!');
    await page.locator('[name=new_password]').fill('Browser-Replaced107!');
    await page.locator('[name=confirm_password]').fill('Browser-Replaced107!');
    await Promise.all([page.waitForURL(url => url.pathname === '/'), page.getByRole('button', { name: 'Change Password', exact: true }).click()]);
    await page.goto(`${base}/customers/new`);
    await page.locator('[name=first_name]').fill('Browser');
    await page.locator('[name=display_name]').fill('Browser Customer 107');
    await Promise.all([page.waitForURL(url => /^\/customers\/\d+\/edit$/.test(url.pathname)), page.locator('form[action="/customers"] button[type=submit]').click()]);
    await page.goto(`${base}/jobs/new`);
    await page.locator('[name=customer_id]').selectOption({ label: 'Browser Customer 107' });
    await page.locator('[name=job_type]').fill('Browser Job 107');
    await Promise.all([page.waitForURL('**/jobs?*'), page.locator('form[action="/jobs"] button[type=submit]').click()]);
    await page.reload();
    assert.match(await page.locator('body').innerText(), /Browser Job 107/);
    await page.goto(`${base}/customers`);
    assert.match(await page.locator('body').innerText(), /Browser Customer 107/);
    console.log('Real executable browser: recovered login, forced replacement, persisted Customer/Job creation and lists passed');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
