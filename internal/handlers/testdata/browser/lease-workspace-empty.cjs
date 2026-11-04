const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
const assertTargets = require('./lease-targets.cjs');
(async () => {
  const [base, admin, reader] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'] });
  try {
    for (const [session, canCreate] of [[admin, true], [reader, false]]) {
      const context = await browser.newContext({ viewport: { width: 320, height: 740 } });
      await context.addCookies([{ name: 'session', value: session, url: base }]);
      const page = await context.newPage();
      await page.goto(base + '/leases?reset=1');
      assert.match(await page.locator('.page-body').innerText(), /No Leases in this view yet/);
      assert.equal(await page.getByRole('link', { name: 'Register Draft', exact: true }).count(), canCreate ? 1 : 0);
      await context.close();
    }
    const context = await browser.newContext({ viewport: { width: 320, height: 740 } });
    await context.addCookies([{ name: 'session', value: reader, url: base }]);
    const page = await context.newPage();
    assert.equal((await context.request.post(base + '/fixture/create?archived=1')).status(), 204);
    await page.goto(base + '/leases?reset=1');
    assert.match(await page.locator('.page-body').innerText(), /No Leases in this view yet/);
    assert.doesNotMatch(await page.locator('.page-body').innerText(), /No Leases match/);
    assert.equal((await context.request.post(base + '/fixture/create')).status(), 204);
    await page.goto(base + '/leases?search=impossible-match');
    assert.match(await page.locator('.page-body').innerText(), /No Leases match these filters/);
    assert.equal(await page.getByRole('link', { name: 'Register Draft', exact: true }).count(), 0);
    await page.getByRole('link', { name: 'Clear filters', exact: true }).focus();
    await page.keyboard.press('Enter');
    await page.waitForURL('**/leases?reset=1');
    assert.match(await page.locator('.page-body').innerText(), /1 visible Leases/);
    for (const coarse of [false, true]) {
      const measured = await browser.newContext({ viewport: { width: 320, height: 740 }, hasTouch: coarse });
      await measured.addCookies([{ name: 'session', value: reader, url: base }]);
      const targetPage = await measured.newPage();
      await targetPage.goto(base + '/leases?search=impossible-match');
      await assertTargets(targetPage, '.page-body a[href^="/leases"]', coarse);
      await measured.close();
    }
    console.log('empty, unmatched, inaccessible archive and Create grant states passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });
