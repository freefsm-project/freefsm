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
    await page.locator('[data-list-save-status]').getByText('Filters remembered', { exact: true }).waitFor();
    const confirmed = await (await context.request.get(base+'/fixture/state')).json();
    assert.ok(confirmed.Revision > 0,'preference was durably committed before revocation');
    assert.match(await page.locator('.page-body').innerText(), /1 visible Leases/);
    assert.equal((await context.request.post(base + '/fixture/revoke')).status(), 204);
    for (let i = 0; i < 2; i++) {
      assert.equal((await page.goto(base + '/leases')).status(), 403, 'remembered forbidden filters must not silently broaden');
      assert.doesNotMatch(await page.locator('.page-body').innerText(), /Restricted archived recovery agreement|restricted recovery failure detail/);
      assert.equal(await page.locator('[data-discovery-results]').count(), 0);
    }
    for (let i=0;i<2;i++) {
      await page.goto(base+'/leases?reset=1');
      assert.deepEqual(await (await context.request.get(base+'/fixture/state')).json(),confirmed,'GET Reset review must not mutate preferences');
    }
    const fields = await page.locator('[data-list-reset]').evaluate(form=>Object.fromEntries(new FormData(form)));
    const reset = await context.request.post(base+'/leases/reset',{
      form:fields,headers:{Referer:page.url(),'X-CSRF-Token':fields.csrf_token},maxRedirects:0,
    });
    assert.equal(reset.status(),303,'explicit CSRF Reset succeeds after authority loss');
    const tombstone = await (await context.request.get(base+'/fixture/state')).json();
    assert.ok(tombstone.Revision > confirmed.Revision,'Reset advances revision');
    assert.equal(Object.keys(tombstone.Values || {}).length,0,'Reset retains an empty tombstone');
    const stale = await context.request.post(base+'/leases/state',{
      form:{csrf_token:fields.csrf_token,revision:String(confirmed.Revision),query:'q='},
      headers:{Referer:page.url(),'X-CSRF-Token':fields.csrf_token,'X-List-State':'save'},
    });
    assert.equal(stale.status(),409,'stale authorized save cannot resurrect preferences after Reset');
    assert.deepEqual(await (await context.request.get(base+'/fixture/state')).json(),tombstone);
    await page.goto(new URL(reset.headers().location,base).href);
    await page.locator('[data-list-save-status]').getByText('Filters remembered', {exact:true}).waitFor();
    for (let i = 0; i < 2; i++) {
      assert.equal((await page.goto(base + '/leases')).status(), 200, 'explicit Reset durably recovers ordinary entry after grant loss');
      await page.locator('[data-list-save-status]').getByText('Filters remembered', {exact:true}).waitFor();
      assert.equal(await page.locator('[name=queue]').inputValue(), '');
      assert.equal(await page.locator('[name=state]').inputValue(), 'active');
      assert.equal(await page.locator('[name=billing]').count(), 0);
      assert.equal(await page.locator('[name=state] option[value=archived]').count(), 0);
      assert.doesNotMatch(await page.locator('.page-body').innerText(), /Generation failures|Monthly billing|Manual review/);
      assert.doesNotMatch(await page.locator('.page-body').innerText(), /Restricted archived recovery agreement|restricted recovery failure detail/);
      assert.match(await page.locator('.page-body').innerText(), /1 visible Leases/);
      assert.equal(await page.getByRole('link',{name:/Recovery visible agreement/}).count(),1,'Reset reveals only the authorized default Lease');
    }
    assert.equal((await page.goto(base + '/leases?' + query)).status(), 403, 'explicit forbidden filter stays forbidden');
    assert.equal((await page.goto(base + '/leases')).status(), 200);
    console.log('persisted authority loss blocks without broadening; explicit Reset durably recovers; explicit forbidden request denied');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });
