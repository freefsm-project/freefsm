const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');
(async () => {
  const [base, cookie] = process.argv.slice(2);
  const browser = await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium-browser', args:['--no-sandbox']});
  try {
    const context = await browser.newContext();
    await context.addCookies([{name:'session',value:cookie,url:base}]);
    const page = await context.newPage();
    let id;
    const enablePosts = [];
    page.on('request', r => {
      if(r.method()==='POST' && new URL(r.url()).pathname.startsWith('/settings/enable-email')) enablePosts.push(r.url());
    });
    await page.route('**/save-actions/*', route => route.abort('connectionreset'));
    await page.route('**/settings/enable-email', async route => {
      if(route.request().method() !== 'POST') return route.continue();
      id = new URLSearchParams(route.request().postData()).get('save_action_id');
      const response = await route.fetch({maxRedirects:0});
      assert.equal(response.status(),303,'real enable must commit before its response is lost');
      assert.equal(response.headers()['x-freefsm-action'],id);
      await route.abort('connectionreset');
    });
    await page.goto(base+'/settings/mail');
    await page.getByRole('button',{name:'Enable outbound email, including eligible queued work',exact:true}).click();
    await page.waitForFunction(() => (sessionStorage.getItem('freefsm-unresolved-actions') || '').includes('unknown'));
    assert.ok(id);
    let retained = await page.evaluate(() => JSON.parse(sessionStorage.getItem('freefsm-unresolved-actions')));
    assert.ok(retained.some(a => a.id===id && a.emailEnable),'lost response retains the original email identity');
    const revoker = await context.newPage();
    revoker.on('dialog', d => d.accept());
    await revoker.goto(base+'/sessions');
    await revoker.getByRole('button',{name:'Revoke all sessions',exact:true}).click();
    await revoker.waitForURL('**/login');
    await page.unroute('**/save-actions/*');
    await page.getByRole('button',{name:'Check save status',exact:true}).click();
    await page.getByText('Session expired—sign in again',{exact:false}).waitFor();
    retained = await page.evaluate(() => JSON.parse(sessionStorage.getItem('freefsm-unresolved-actions')));
    assert.ok(retained.some(a => a.id===id),'revocation must preserve unresolved identity');
    assert.equal(await page.getByRole('button',{name:'Retry same email enable action',exact:true}).count(),0);
    await page.goto(base+'/login');
    await page.locator('[name=email]').fill('admin@test');
    await page.locator('[name=password]').fill('PermissionFixture1!');
    await page.locator('button[type=submit]').click();
    await page.waitForURL(base+'/');
    await page.goto(base+'/settings/mail');
    assert.equal(enablePosts.length,1,'fresh login and navigation must not replay enable');
    console.log('PASS lost real committed response; same identity '+id+' retained across unknown status and actual session revocation; real fresh login did not replay');
  } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exit(1); });
