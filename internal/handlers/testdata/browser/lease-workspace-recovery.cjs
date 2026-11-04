const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
(async () => {
  const [base, session, query] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({ viewport: { width: 320, height: 740 } });
    await context.addCookies([{ name: 'session', value: session, url: base }]);
    const page = await context.newPage();
    assert.equal((await page.goto(base + '/leases?' + query)).status(), 200);
    assert.match(await page.locator('.page-body').innerText(), /1 visible Leases/);
    assert.equal((await context.request.post(base + '/fixture/revoke')).status(), 204);
    for (let i = 0; i < 2; i++) {
      assert.equal((await page.goto(base + '/leases')).status(), 200, 'ordinary entry must recover after grant loss');
      assert.equal(await page.locator('[name=queue]').inputValue(), '');
      assert.equal(await page.locator('[name=state]').inputValue(), 'active');
      assert.equal(await page.locator('[name=billing]').count(), 0);
      assert.equal(await page.locator('[name=state] option[value=archived]').count(), 0);
      assert.doesNotMatch(await page.locator('.page-body').innerText(), /Generation failures|Monthly billing|Manual review/);
      assert.doesNotMatch(await page.locator('.page-body').innerText(), /Restricted archived recovery agreement|restricted recovery failure detail/);
      assert.match(await page.locator('.page-body').innerText(), /1 visible Leases/);
    }
    assert.equal((await page.goto(base + '/leases?' + query)).status(), 403, 'explicit forbidden filter stays forbidden');
    assert.equal((await page.goto(base + '/leases')).status(), 200);
    console.log('persisted authority loss recovers; explicit request denied');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });
