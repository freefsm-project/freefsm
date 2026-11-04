const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
const assertTargets = require('./lease-targets.cjs');
(async () => {
  const [base, admin, reader, path, archived] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'] });
  try {
    for (const coarse of [false, true]) {
      const context = await browser.newContext({ viewport: { width: 320, height: 740 }, hasTouch: coarse });
      await context.addCookies([{ name: 'session', value: admin, url: base }]);
      const page = await context.newPage();
      await page.goto(base + path);
      const history = page.locator('[aria-label="Lease relationship history"]');
      assert.equal(await history.locator(':scope > li').count(), 25);
      assert.match(await page.locator('[aria-label="Current Lease coverage"]').innerText(), /Newest current coverage/);
      await assertTargets(page, '[aria-label="Related Leases"] a', coarse);
      await page.getByRole('link', { name: 'Next coverage', exact: true }).focus();
      await page.keyboard.press('Enter');
      await page.waitForURL(url => url.searchParams.get('coverage_page') === '2');
      assert.equal(await history.locator(':scope > li').count(), 3);
      assert.match(await history.innerText(), /Historical coverage 26/);
      assert.equal(await history.locator(`a[href="/leases/${archived}"]`).count(), 1);
      const stable = await history.innerText();
      await page.reload();
      assert.equal(await history.innerText(), stable);
      await assertTargets(page, '[aria-label="Related Leases"] a', coarse);
      await page.getByRole('link', { name: 'Previous coverage', exact: true }).click();
      await page.waitForURL(url => url.searchParams.get('coverage_page') === '1');
      for (const size of ['50', '100']) {
        await page.locator('[name=coverage_per_page]').selectOption(size);
        await page.getByRole('button', { name: 'Apply coverage page size', exact: true }).click();
        await page.waitForURL(url => url.searchParams.get('coverage_per_page') === size);
        assert.equal(await history.locator(':scope > li').count(), 28);
        assert.equal(await page.getByRole('link', { name: 'Next coverage' }).count(), 0);
      }
      await context.close();
    }
    const context = await browser.newContext();
    await context.addCookies([{ name: 'session', value: reader, url: base }]);
    const page = await context.newPage();
    await page.goto(base + path + '?coverage_page=2');
    const row = page.locator('[aria-label="Lease relationship history"] li').filter({ hasText: 'Historical coverage 26' });
    assert.equal(await row.count(), 1);
    assert.match(await row.innerText(), /Archived/);
    assert.doesNotMatch(await row.innerText(), /Coverage:|Billing/);
    assert.equal(await row.locator('a').count(), 0);
    assert.equal((await page.goto(base + '/leases/' + archived)).status(), 404);
    console.log('current coverage, complete stable pages and restricted archived references passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });
