const assert = require('node:assert/strict');
const {chromium} = require('playwright-core');
(async () => {
 const [base,cookie,deniedCookie,lease,obligation,viewerCookie]=process.argv.slice(2);
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try {
  async function pageFor(value){const context=await browser.newContext();await context.addCookies([{name:'session',value,url:base,httpOnly:true}]);return context.newPage();}
  const page=await pageFor(cookie),denied=await pageFor(deniedCookie);
  const period=`/leases/${lease}/billing/periods/${obligation}`;
  const viewer=await pageFor(viewerCookie);
  assert.equal((await viewer.goto(base+period)).status(),200);
  assert.match(await viewer.content(),/Draft generation failed/);
  assert.equal(await viewer.getByRole('button',{name:'Retry reviewed obligation',exact:true}).count(),0);
  assert.equal((await viewer.goto(base+`/leases/${lease}`)).status(),404);
  assert.equal((await denied.goto(base+period)).status(),404);
  assert.doesNotMatch(await denied.content(),/4321|Draft generation failed/);
  assert.equal((await page.goto(base+`/leases/${lease}/billing`)).status(),200);
  assert.match(await page.content(),/Draft generation failed/);
  await page.goto(base+period);
  assert.match(await page.content(),/4321/);
  let response=page.waitForResponse(r=>r.request().method()==='POST'&&r.url().endsWith('/retry'));
  await page.getByRole('button',{name:'Retry reviewed obligation',exact:true}).click();
  assert.equal((await response).status(),409);
  assert.equal((await page.context().request.post(base+'/test/repair')).status(),204);
  await page.goto(base+period);
  async function loseThenReconcile(suffix,button){
   const fields=await page.locator(`form[action$="/${suffix}"]`).evaluate(f=>Object.fromEntries([...new FormData(f)].filter(([key])=>['csrf_token','review_hash','operation_key','invoice_id'].includes(key))));
   let delivered;const committed=new Promise(resolve=>{delivered=resolve;});
   await page.route('**'+period+'/'+suffix,async route=>{
    if(route.request().method()!=='POST')return route.continue();
    const result=await route.fetch({maxRedirects:0});assert.equal(result.status(),303);
    await route.abort('failed');delivered();
   });
   await page.getByRole('button',{name:button,exact:true}).click();await committed;
   await page.unroute('**'+period+'/'+suffix);
   await page.getByRole('button',{name:button,exact:true}).waitFor({state:'detached'});
   await page.getByText(/Generation receipt: Invoice/).waitFor();
   assert.match(await page.content(),/Generation receipt: Invoice/);
   const replay=await page.context().request.post(base+period+'/'+suffix,{form:fields,headers:{'Sec-Fetch-Site':'same-origin'},maxRedirects:0});
   assert.equal(replay.status(),303,await replay.text());
   await page.reload();
  }
  await loseThenReconcile('retry','Retry reviewed obligation');
  assert.equal(await page.getByRole('button',{name:'Retry reviewed obligation',exact:true}).count(),0);
  assert.equal((await page.context().request.post(base+'/test/void')).status(),204);
  await page.reload();assert.match(await page.content(),/Review replacement/);
  await loseThenReconcile('replace','Replace reviewed Void generation');
  assert.equal(await page.getByRole('button',{name:'Replace reviewed Void generation',exact:true}).count(),0);
  assert.equal((await page.context().request.post(base+'/test/coverage')).status(),204);
  await page.reload();assert.match(await page.content(),/Manual review required/);
  await viewer.goto(base+period);assert.match(await viewer.content(),/Manual review required/);
  assert.equal((await denied.goto(base+period)).status(),404);
  console.log('billing-only review; base-only denial; visible failure; explicit retry and replacement; real lost responses reconciled to durable receipts; same-operation replay');
 } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exit(1);});
