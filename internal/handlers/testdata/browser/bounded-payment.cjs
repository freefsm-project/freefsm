'use strict';
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
(async () => {
 const [base,cookie,id]=process.argv.slice(2);
 const browser=await chromium.launch({headless:true,executablePath:process.env.BROWSER_EXECUTABLE,args:['--no-sandbox']});
 try {
  const context=await browser.newContext({viewport:{width:390,height:844}});
  await context.addCookies([{name:'session',value:cookie,url:base}]);
  const page=await context.newPage();
  page.on('dialog',dialog=>dialog.accept());
  await page.goto(`${base}/settings/payment-methods`);
  const labels=page.locator("form[action='/settings/payment-methods/cash']");
  await labels.locator('[name=label]').fill('Counter cash');
  await Promise.all([page.waitForNavigation(),labels.getByRole('button',{name:'Save label',exact:true}).click()]);
  await page.goto(`${base}/invoices/${id}`);
  assert.equal(await page.locator('option[value=cash]').textContent(),'Counter cash');
  const form=page.locator(`form[action='/invoices/${id}/payments']`);
  await form.locator('[name=amount]').fill('25.00');
  await form.locator('[name=method]').selectOption('check');
  await form.locator('[name=reference]').fill('browser-check');
  let posts=0;
  await page.route(`**/invoices/${id}/payments`,async route=>{
   if(route.request().method()!=='POST')return route.continue();
   posts++;if(posts===1){await route.fetch();await route.abort('failed');}else {
    if(posts===3)assert.equal((await page.request.post(base+'/fixture/revoke-payment-session')).status(),200);
    await route.continue();
   }
  });
  await form.getByRole('button',{name:'Record Payment',exact:true}).click();
  await page.getByText('Payment status unknown. Check the original receipt before another Payment.',{exact:true}).waitFor();
  assert.equal(posts,1);
  await page.reload();
  await page.route('**/payment-operations/*',async route=>{const response=await route.fetch();const body=await response.json();body.data.outcome='future_outcome';await route.fulfill({response,json:body});});
  await page.getByRole('button',{name:'Check original Payment receipt',exact:true}).click();
  await page.getByText('Unrecognized Payment receipt. Keep the original identity locked for review.',{exact:true}).waitFor();
  assert.equal(await page.getByRole('button',{name:'Review a new Payment',exact:true}).count(),0);
  await page.unroute('**/payment-operations/*');
  await page.getByRole('button',{name:'Check original Payment receipt',exact:true}).click();
  await page.getByText('Payment accepted. The original receipt was confirmed; do not record it again.',{exact:true}).waitFor();
  assert.equal(posts,1);
  await Promise.all([page.waitForNavigation(),page.getByRole('button',{name:'Record a different Payment',exact:true}).click()]);
  const second=page.locator(`form[action='/invoices/${id}/payments']`);
  await second.locator('[name=amount]').fill('25.00');
  await second.getByRole('button',{name:'Record Payment',exact:true}).click();
  await page.getByText('Payment accepted. The original receipt was confirmed; do not record it again.',{exact:true}).waitFor();
  assert.equal(posts,2);
  await Promise.all([page.waitForNavigation(),page.getByRole('button',{name:'Record a different Payment',exact:true}).click()]);
  const denied=page.locator(`form[action='/invoices/${id}/payments']`);
  await denied.locator('[name=amount]').fill('10.00');
  await denied.locator('[name=notes]').fill('private revoked payment input');
  await denied.getByRole('button',{name:'Record Payment',exact:true}).click();
  await denied.waitFor({state:'detached'});
  assert.equal(await page.locator('[name=notes]').count(),0);
  const retained=await page.evaluate(()=>sessionStorage.getItem('freefsm.payment.pending-operation'));
  assert.ok(retained&&!retained.includes('private revoked payment input'));
  console.log('Label editor, lost response and explicitly reviewed second Payment: exactly two receipts.');
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exit(1)});
