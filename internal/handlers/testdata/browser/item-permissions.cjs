const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');

(async () => {
  const [base, readerToken, adminToken] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext();
    await context.addCookies([{ name: 'session', value: readerToken, url: base }]);
    const page = await context.newPage();
    await page.goto(`${base}/items`);
    await page.getByRole('button', { name: 'New Item', exact: true }).click();
    assert.equal(await page.locator('[name=unit_cost]').count(), 0);
    assert.equal(await page.locator('[name=unit_price]').count(), 0);
    await page.locator('[name=name]').fill('Browser Item created');
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await page.waitForURL(/\/items\/\d+$/);
    const itemURL = page.url();
    assert.equal((await page.textContent('body')).includes('Unit Cost'), false);
    const adminContext = await browser.newContext();
    await adminContext.addCookies([{ name: 'session', value: adminToken, url: base }]);
    const admin = await adminContext.newPage();
    await admin.goto(`${itemURL}/edit`);
    await admin.locator('[name=sku]').fill('BROWSER-43');
    await admin.locator('[name=unit_price]').fill('45.67');
    await admin.locator('[name=unit_cost]').fill('987.65');
    await admin.locator('[name=type]').selectOption('product');
    await admin.getByRole('button', { name: 'Save', exact: true }).click();
    await admin.waitForURL(itemURL);
    assert.match(await admin.textContent('body'), /987.65/);
    await page.goto(`${itemURL}/edit`);
    assert.equal(await page.locator('[name=unit_cost]').count(), 0);
    await page.locator('[name=name]').fill('Browser Item retained');
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await page.waitForURL(itemURL);
    const body = await page.textContent('body');
    for (const secret of ['987.65', '45.67', 'BROWSER-43', 'Unit Cost']) assert.equal(body.includes(secret), false);
    await admin.goto(itemURL);
    const saved = await admin.textContent('body');
    for (const fact of ['987.65', '45.67', 'BROWSER-43', 'Browser Item retained', 'product']) assert.equal(saved.includes(fact), true, `missing ${fact}: ${saved}`);
    console.log('Persisted Item creation, independent facet edits and Cost denial passed.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
