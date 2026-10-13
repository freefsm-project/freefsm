const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');
(async () => {
  const [base, cookie, customer] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium', headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = await context.newPage();
    page.on('dialog', dialog => dialog.accept());
    await page.goto(base + '/projects/new');
    await page.locator('[name=customer_id]').selectOption(customer);
    await page.locator('[name=name]').fill('Persisted Project contract');
    assert.equal(await page.locator('[name=completion_percentage]').count(), 0);
    await Promise.all([page.waitForURL(/\/projects\?/), page.getByRole('button', { name: 'Save', exact: true }).click()]);
    await page.getByRole('link', { name: 'Persisted Project contract', exact: true }).first().click();
    await page.waitForURL(/\/projects\/\d+$/);
    const project = page.url();
    const initialStatus = await page.locator('form[action$="/status"] [name=status_id]').inputValue();
    assert.match(await page.locator('body').innerText(), /0%/);
    await page.goto(project + '/edit');
    assert.equal(await page.locator('select[name=customer_id]').isDisabled(), true);
    await page.locator('[name=completion_percentage]').fill('37');
    await page.locator('[name=start_time]').fill('2020-01-01');
    await page.locator('[name=end_time]').fill('2020-01-02');
    await page.locator('[name=service_notes]').fill('Persisted service instruction');
    await Promise.all([page.waitForURL(/Project\+updated/), page.getByRole('button', { name: 'Save', exact: true }).click()]);
    await page.reload();
    assert.match(await page.locator('body').innerText(), /37%/);
    assert.match(await page.locator('body').innerText(), /Overdue/);
    assert.match(await page.locator('body').innerText(), /2020-01-02/);
    // Use the rendered status form and CSRF token, preserving the production command.
    const statusForm = page.locator('form[action$="/status"]');
    await statusForm.locator('[name=status_id]').selectOption({ label: 'Completed' });
    await Promise.all([page.waitForNavigation(), statusForm.locator('button[type=submit]').click()]);
    assert.match(await page.locator('body').innerText(), /100%/);
    assert.doesNotMatch(await page.locator('body').innerText(), /Overdue/);
    await page.goto(project + '/edit');
    assert.equal(await page.locator('[name=completion_percentage]').count(), 0);
    await page.goto(project);
    const reopen = page.locator('form[action$="/status"]');
    await reopen.locator('[name=status_id]').selectOption(initialStatus);
    await Promise.all([page.waitForNavigation(), reopen.locator('button[type=submit]').click()]);
    await page.reload();
    assert.match(await page.locator('body').innerText(), /37%/);
    assert.match(await page.locator('body').innerText(), /Persisted service instruction/);
    console.log('Persisted mobile Project creation, immutable Customer, manual progress, local dates, overdue, completion and reopening passed.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
