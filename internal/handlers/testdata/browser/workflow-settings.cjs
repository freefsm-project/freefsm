const { chromium } = require('playwright-core');
(async () => {
  const [base, session] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext();
    await context.addCookies([{ name: 'session', value: session, url: base }]);
    const page = await context.newPage();
    await page.goto(base + '/settings/statuses/job');
    await page.getByRole('button', { name: 'Create Custom Status', exact: true }).click();
    const dialog = page.locator('#status-create-dialog');
    await dialog.locator('[name=category]').selectOption('job:pending');
    await dialog.locator('[name=name]').fill('Browser persisted status');
    await Promise.all([page.waitForURL(/flash=Status/), dialog.getByRole('button', { name: 'Create status', exact: true }).click()]);
    await page.reload();
    await page.getByRole('button', { name: 'Edit Browser persisted status status', exact: true }).waitFor();
    await page.getByRole('button', { name: 'Edit Browser persisted status status', exact: true }).click();
    const edit = page.locator('dialog[open]');
    await edit.locator('input[name=name]').fill('Browser renamed status');
    await Promise.all([page.waitForURL(/flash=Status\+updated/), edit.locator('form').first().locator('button[type=submit]').click()]);
    await page.reload();
    await page.getByRole('button', { name: 'Edit Browser renamed status status', exact: true }).waitFor();
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });
