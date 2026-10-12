const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');
(async () => {
  const [base, cookie, customer] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium', headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext();
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = await context.newPage();
    assert.equal((await page.goto(`${base}/customers/${customer}`)).status(), 200);
    const widget = page.locator(`#comments-customer-${customer}`);
    await widget.locator('.comments-form textarea').fill('Browser original\n<script>plain text</script>');
    await widget.getByRole('button', { name: 'Add Comment', exact: true }).click();
    await widget.getByText('Browser original', { exact: false }).waitFor();
    assert.equal(await widget.locator('script').count(), 0);
    await widget.locator('summary').click();
    await widget.locator('details textarea').fill('Browser reply persists');
    await widget.getByRole('button', { name: 'Add Reply', exact: true }).click();
    await widget.getByText('Browser reply persists', { exact: true }).waitFor();
    page.on('dialog', dialog => dialog.accept());
    await widget.locator('.comments-delete-button').first().click();
    await widget.getByText('Removed by Browser author', { exact: false }).waitFor();
    await page.reload();
    await widget.getByText('Browser reply persists', { exact: true }).waitFor();
    assert.doesNotMatch(await widget.innerText(), /Browser original|plain text/);
    assert.equal(await widget.locator('summary').count(), 0);
    console.log('Persisted plain-text multiline Comment, one-level reply, attributed removal, preserved reply and closed thread across reload; no parent Edit grant.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
