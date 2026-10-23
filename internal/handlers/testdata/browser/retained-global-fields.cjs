const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');

(async () => {
  const [base, cookie, customerID] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = await context.newPage();
    page.on('dialog', dialog => dialog.accept());
    assert.equal((await page.goto(base + '/estimates/new')).status(), 200);
    await page.locator('[name=title]').fill('Global browser estimate');
    await page.locator('[name=customer_id]').selectOption(customerID);
    await page.locator('[name=custom_field_9600]').selectOption('North');
    await Promise.all([page.waitForURL(url => url.pathname === '/estimates'), page.getByRole('button', { name: 'Save', exact: true }).click()]);
    await page.getByRole('link', { name: 'Global browser estimate', exact: true }).first().click();
    await page.waitForURL(/\/estimates\/\d+$/);
    const estimate = page.url();
    await page.getByRole('button', { name: 'Convert to Invoice', exact: true }).click();
    await page.getByRole('heading', { name: 'Review Invoice Custom Fields', exact: true }).waitFor();
    assert.match(await page.locator('body').innerText(), /North/);
    await Promise.all([page.waitForURL(/\/invoices\/\d+(?:\?|$)/), page.getByRole('button', { name: 'Convert to Invoice', exact: true }).click()]);
    const invoice = page.url().split('?')[0];
    await page.goto(invoice + '/custom-fields');
    assert.equal(await page.locator('[name=custom_field_9601]').inputValue(), 'North');
    await page.locator('[name=custom_field_9601]').selectOption('South');
    await Promise.all([page.waitForNavigation(), page.getByRole('button', { name: 'Save Custom Fields', exact: true }).click()]);
    await page.goto(base + '/settings/custom-fields/9601/edit');
    await page.locator('[name=conversion_key]').fill('next-browser-cycle');
    await Promise.all([page.waitForURL(base + '/settings/custom-fields'), page.getByRole('button', { name: 'Save', exact: true }).click()]);
    await page.goto(invoice);
    await page.getByRole('link', { name: 'Review reversion to Estimate', exact: true }).click();
    await page.waitForURL(invoice + '/revert-to-estimate');
    assert.match(await page.locator('[name=cycle_id]').inputValue(), /^[0-9a-f-]{36}$/);
    assert.match(await page.locator('[name=revision]').inputValue(), /^[a-f0-9]{64}$/);
    await Promise.all([page.waitForURL(/\/estimates\/\d+(?:\?|$)/), page.getByRole('button', { name: 'Revert to Estimate', exact: true }).click()]);
    await page.goto(estimate + '/custom-fields');
    assert.equal(await page.locator('[name=custom_field_9600]').inputValue(), 'South');
    await page.reload();
    assert.equal(await page.locator('[name=custom_field_9600]').inputValue(), 'South');
    console.log('Persisted retained-global upgrade: preview, conversion, independent Invoice edit, pairing change, captured-cycle reversion and reload preserve South.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
