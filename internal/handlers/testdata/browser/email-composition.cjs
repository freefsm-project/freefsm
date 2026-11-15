const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');
(async () => {
  const [base, cookie, id] = process.argv.slice(2);
  const browser = await chromium.launch({executablePath: process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium-browser', args:['--no-sandbox']});
  try {
    const context = await browser.newContext();
    await context.addCookies([{name:'session',value:cookie,url:base}]);
    const page = await context.newPage();
    await page.goto(base+'/settings/mail');
    await page.evaluate(id => sessionStorage.setItem('freefsm-unresolved-actions', JSON.stringify([{id,url:'/save-actions/'+id,state:'unknown',history:'',emailEnable:true}])), id);
    const posts = [];
    page.on('request', r => { if(r.method()==='POST') posts.push(new URL(r.url()).pathname); });
    page.on('response', async r => { if(r.request().method()==='POST') console.log('POST response',new URL(r.url()).pathname,r.status(),await r.text()); });
    page.on('pageerror', e => console.log('PAGE ERROR',String(e)));
    await page.reload();
    await page.getByRole('button',{name:'Check save status',exact:true}).click();
    await page.getByRole('button',{name:'Reconcile this email enable action',exact:true}).waitFor();
    assert.deepEqual(posts, [], 'status and reload never replay enable');
    assert.equal(await page.getByRole('button',{name:'Retry same email enable action',exact:true}).count(),0);
    await page.getByRole('button',{name:'Reconcile this email enable action',exact:true}).click();
    await page.getByRole('button',{name:'Retry same email enable action',exact:true}).waitFor();
    assert.deepEqual(posts, ['/settings/enable-email/'+id+'/reconcile']);
    await page.getByRole('button',{name:'Retry same email enable action',exact:true}).click();
    await page.locator('[data-action-state="saved"]').waitFor();
    assert.match(await page.locator('[data-action-state="saved"]').textContent(), /Outbound email enablement confirmed/);
    assert.deepEqual(posts, ['/settings/enable-email/'+id+'/reconcile','/settings/enable-email/'+id+'/retry']);
    await page.getByRole('button',{name:'View current mail status',exact:true}).click();
    await page.waitForURL(base+'/settings/mail');
    assert.equal(await page.getByRole('button',{name:'Enable outbound email, including eligible queued work',exact:true}).count(),0);
    console.log('PASS actual rendered interaction.js, real routed reconcile/token-CAS retry, stable identity and no automatic replay; admission seeded by test fixture, not end-to-end lost transport');
  } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exit(1); });
