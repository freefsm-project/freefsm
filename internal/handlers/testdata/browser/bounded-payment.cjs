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
  await page.goto(`${base}/invoices/${id}`);
  const form=page.locator(`form[action='/invoices/${id}/payments']`);
  await form.locator('[name=amount]').fill('25.00');
  await form.locator('[name=method]').selectOption('check');
  await form.locator('[name=reference]').fill('browser-check');
  let posts=0;
  await page.route(`**/invoices/${id}/payments`,async route=>{
   if(route.request().method()!=='POST')return route.continue();
   posts++;await route.fetch();await route.abort('failed');
  });
  await form.getByRole('button',{name:'Record Payment',exact:true}).click();
  await page.getByText('Payment status unknown. Check the original receipt before another Payment.',{exact:true}).waitFor();
  assert.equal(posts,1);
  await page.reload();
  await page.getByRole('button',{name:'Check original Payment receipt',exact:true}).click();
  await page.getByText('Payment accepted. The original receipt was confirmed; do not record it again.',{exact:true}).waitFor();
  assert.equal(posts,1);
  console.log('Persisted lost-response/reload reconciliation: one fixed Payment, no replay.');
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exit(1)});
