const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');

(async () => {
  const [base, cookie] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium', headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({ viewport: { width: 320, height: 844 }, acceptDownloads: true });
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = await context.newPage();
    await page.goto(base + '/settings/audit');
    await page.getByRole('heading', { name: 'Protected administrative audit', exact: true }).waitFor();
    assert.equal(await page.getByLabel('Sorting', { exact: true }).inputValue(), 'newest', 'displayed sorting must match applied default');
    assert.equal(await page.getByLabel('Rows per page', { exact: true }).inputValue(), '25', 'displayed page size must match applied default');
    assert.ok(await page.getByRole('region', {name: 'Audit evidence', exact: true}).evaluate(el => el.scrollWidth > el.clientWidth), 'dense evidence must scroll horizontally');
    assert.ok(await page.locator('tbody tr').first().evaluate(el => el.getBoundingClientRect().height < 100), 'evidence must stay readable, not wrap one character per line');
    await page.getByLabel('Permission', { exact: true }).fill('customer.create');
    await page.getByLabel('Event-time Role', { exact: true }).fill('Administrator');
    await page.getByLabel('Sorting', { exact: true }).selectOption('oldest');
    await page.getByRole('button', { name: 'Filter audit', exact: true }).click();
    await page.waitForURL(/permission=customer.create/);
    assert.equal(await page.locator('tbody tr').count(), 1);
    assert.match(await page.locator('tbody').innerText(), /customer.create/);
    assert.doesNotMatch(await page.locator('tbody').innerText(), /Browser audit fixture/);
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), '320px contained audit table');
    await page.getByRole('link', { name: 'Review audit export', exact: true }).click();
    await page.getByRole('heading', { name: 'Export protected audit', exact: true }).waitFor();
    assert.match(await page.locator('main').innerText(), /All matching rows/);
    assert.match(await page.locator('main').innerText(), /Event-time Role Administrator/);
    await Promise.all([page.waitForURL(/\/exports\/\d+$/), page.getByRole('button', { name: 'Confirm export', exact: true }).click()]);
    const path = new URL(page.url()).pathname;
    await page.goto(base + '/settings/audit');
    await page.getByRole('link', { name: 'My CSV exports', exact: true }).click();
    await page.locator(`a[href="${path}"]`).click();
    for (let i = 0; i < 30 && !(await page.getByRole('link', { name: 'Download CSV', exact: true }).count()); i++) {
      await page.waitForTimeout(250); await page.reload();
    }
    assert.match(await page.locator('main').innerText(), /Completed: 1 rows/);
    const [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('link', { name: 'Download CSV', exact: true }).click()]);
    assert.equal(await download.failure(), null);
    const stream = await download.createReadStream();
    let csv = ''; for await (const chunk of stream) csv += chunk;
    assert.match(csv, /event_role_name/); assert.match(csv, /customer.create/); assert.match(csv, /Administrator/);
    assert.doesNotMatch(csv, /Browser audit fixture/);
    await page.goto(base + '/settings/audit?permission=audit.export&channel=worker');
    assert.match(await page.locator('tbody').innerText(), /system/);
    await page.getByLabel('Permission', { exact: true }).focus();
    await page.keyboard.press('Tab');
    assert.equal(await page.getByLabel('Scope', { exact: true }).evaluate(el => el === document.activeElement), true);
    if (process.env.AUDIT_SCREENSHOT) await page.screenshot({path: process.env.AUDIT_SCREENSHOT, fullPage: true});
    await page.setViewportSize({width: 1280, height: 800});
    await page.reload();
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth));
    console.log('Persisted 320px/desktop Chromium: event-role filter, contained evidence, keyboard filters, explicit confirmation, durable navigation, authenticated CSV, system/initiator evidence.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });
