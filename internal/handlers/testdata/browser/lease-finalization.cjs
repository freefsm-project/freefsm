const assert = require('node:assert/strict');
const {chromium} = require('playwright-core');
(async () => {
 const [base, cookie, deniedCookie, id, date] = process.argv.slice(2);
 const browser = await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try {
  async function pageFor(value) {const c=await browser.newContext();await c.addCookies([{name:'session',value,url:base,httpOnly:true}]);return c.newPage();}
  const page=await pageFor(cookie), denied=await pageFor(deniedCookie), path=`/leases/${id}/finalize`;
  assert.equal((await denied.goto(base+path)).status(),403);
  assert.doesNotMatch(await denied.content(),/12345/);
  assert.equal((await page.goto(base+path)).status(),200);
  assert.match(await page.content(),/is unplaced/);
  assert.match(await page.content(),/12345/);
  await page.getByLabel('Managed billing start').fill(date);
  // Deliver the command to the server but lose its response at the transport.
  let lost;
  const responseLost=new Promise(resolve=>{lost=resolve;});
  await page.route('**'+path,async route=>{
   if(route.request().method()!=='POST')return route.continue();
   const response=await route.fetch({maxRedirects:0});assert.equal(response.status(),303);await route.abort('failed');lost();
  });
  await page.getByRole('button',{name:'Finalize Lease',exact:true}).click();
  await responseLost;
  // Shared save reconciliation must resolve the lost response to the receipt.
  await page.unroute('**'+path);
  await page.getByText('Finalization receipt',{exact:true}).waitFor();
  await page.reload();await page.getByText('Finalization receipt',{exact:true}).waitFor();
  assert.match(await page.content(),new RegExp(date));
  assert.equal(await page.getByRole('button',{name:'Finalize Lease',exact:true}).count(),0);
  const token=await page.locator('input[name=csrf_token]').first().inputValue();
  const replay=await page.context().request.post(base+path,{form:{csrf_token:token,revision:'1',billing_revision:'1',managed_billing_start:date},headers:{'Sec-Fetch-Site':'same-origin'},maxRedirects:0});
  assert.equal(replay.status(),303);
  await page.reload();await page.getByText('Finalization receipt',{exact:true}).waitFor();
  await page.goto(base+`/leases/${id}/billing`);
  assert.match(await page.content(),/Finalized billing terms/);
  assert.equal(await page.getByRole('button',{name:'Save Billing terms',exact:true}).count(),0);
  console.log('restricted grants; review terms/unplaced warning; lost response persisted receipt; exact replay');
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exit(1)});
