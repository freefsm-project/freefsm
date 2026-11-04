const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
const assertTargets = require('./lease-targets.cjs');
(async () => {
  const [base, session, customer, location] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'] });
  try {
    for (const coarse of [false, true]) {
      const measured = await browser.newContext({ viewport: { width: 320, height: 740 }, hasTouch: coarse });
      await measured.addCookies([{ name: 'session', value: session, url: base }]);
      const targetPage = await measured.newPage();
      await targetPage.goto(base + '/leases?reset=1');
      await assertTargets(targetPage, '.page-body a[href^="/leases"]', coarse);
      await targetPage.goto(base + '/leases?page=2');
      await assertTargets(targetPage, '.page-body a[href^="/leases"]', coarse);
      await targetPage.goto(base + '/leases?facet=billing');
      await assertTargets(targetPage, '.page-body a[href^="/leases"]', coarse);
      for (const route of ['/customers/' + customer, '/locations/' + location]) {
        await targetPage.goto(base + route);
        await assertTargets(targetPage, '[aria-label="Related Leases"] a', coarse);
      }
      await measured.close();
    }
    const context = await browser.newContext({ viewport: { width: 320, height: 740 } });
    await context.addCookies([{ name: 'session', value: session, url: base }]);
    const page = await context.newPage();
    await page.goto(base + '/leases?queue=starts&per_page=50&search=agreement');
    await page.getByRole('link', { name: /Workspace agreement/ }).click();
    await page.goto(base + '/leases');
    assert.equal(await page.locator('[name=queue]').inputValue(), 'starts');
    assert.equal(await page.locator('[name=per_page]').inputValue(), '50');
    assert.equal(await page.locator('[name=search]').inputValue(), 'agreement');
    await page.reload();
    assert.equal(await page.locator('[name=queue]').inputValue(), 'starts');
    await page.goto(base + '/leases?queue=failed');
    assert.equal(await page.locator('[name=queue]').inputValue(), 'failed');
    await page.getByRole('link', { name: 'Reset filters' }).focus();
    await page.keyboard.press('Enter');
    await page.waitForURL('**/leases?reset=1');
    assert.equal(await page.locator('[name=queue]').inputValue(), '');
    await page.goto(base + '/leases');
    assert.equal(await page.locator('[name=search]').inputValue(), '');
    await page.getByRole('link', { name: 'Next', exact: true }).click();
    await page.waitForURL(url => url.searchParams.get('page') === '2');
    assert.equal(new URL(page.url()).searchParams.get('page'), '2');
    await page.getByRole('link', { name: 'Previous', exact: true }).click();
    await page.waitForURL(url => url.searchParams.get('page') === '1');
    assert.equal(new URL(page.url()).searchParams.get('page'), '1');
    await page.locator('[name=per_page]').selectOption('100');
    await page.getByRole('button', { name: 'Apply', exact: true }).click();
    await page.waitForURL(url => url.searchParams.get('per_page') === '100');
    assert.equal(await page.getByRole('link', { name: 'Next', exact: true }).count(), 0);
    console.log('retained query, URL override, reload and keyboard reset passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });
