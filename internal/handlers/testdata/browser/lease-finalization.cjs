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
  // A real validation error must not finalize; a fresh review remains usable.
  await page.getByLabel('Managed billing start').fill('');
  await page.locator('form').filter({has:page.getByRole('button',{name:'Finalize Lease',exact:true})}).evaluate(f=>f.noValidate=true);
  let rejected=page.waitForResponse(r=>r.request().method()==='POST'&&r.url()===base+path);
  await page.getByRole('button',{name:'Finalize Lease',exact:true}).click();
  assert.equal((await rejected).status(),409);
  await page.goto(base+path);
  for (const loss of ['grant','session']) {
   await page.getByLabel('Managed billing start').fill(date);
   if(loss==='grant')assert.equal((await page.context().request.post(base+'/test/billing-grant')).status(),204);
   else await page.context().clearCookies({name:'session'});
   await page.getByRole('button',{name:'Finalize Lease',exact:true}).click();
   try { await page.getByRole('alert').filter({hasText:'Inaccessible input has been removed'}).waitFor(); }
   catch(error) { console.error(loss, await page.locator('body').innerText()); throw error; }
   assert.doesNotMatch(await page.content(),/12345/);
   assert.equal(await page.locator('[name=managed_billing_start]').count(),0);
   if(loss==='grant')assert.equal((await page.context().request.post(base+'/test/billing-grant?restore=1')).status(),204);
   else await page.context().addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
   assert.equal((await page.goto(base+path)).status(),200);
  }
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
   assert.match(await page.locator('main').innerText(),/Original finalized billing terms — read-only\./);
   assert.equal(await page.getByRole('link',{name:'Revise billing and view effective terms',exact:true}).count(),1);
  assert.equal(await page.getByRole('button',{name:'Save Billing terms',exact:true}).count(),0);
  console.log('restricted grants; validation rejection; real grant and session loss purge billing/input; review terms/unplaced warning; lost response persisted receipt; exact replay');
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exit(1)});
