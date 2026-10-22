'use strict';
const {chromium}=require('playwright-core');
const assert=require('node:assert/strict');
(async()=>{
 const [base,cookie,customer,invoice,scenario]=process.argv.slice(2);
 const browser=await chromium.launch({headless:true,executablePath:process.env.BROWSER_EXECUTABLE,args:['--no-sandbox']});
 try {
  const context=await browser.newContext();await context.addCookies([{name:'session',value:cookie,url:base}]);
  const page=await context.newPage();page.on('dialog',d=>d.accept());
  const path='/customers/'+customer+'/payments';await page.goto(base+path);
  const form=page.locator('form[data-office-payment]');
  assert((await page.content()).includes('PRIVATE HISTORICAL REFERENCE'));
  assert.equal(await form.locator(`option[value="${invoice}"]`).count(),1);
  await form.locator('[name=amount]').fill('1.23');await form.locator('[name=reference]').fill('PRIVATE LOCAL INPUT');
  const op=await form.locator('[name=idempotency_key]').inputValue();
  let posts=0, holdContext=false, releaseContext, contextStarted;
  const contextGate=new Promise(resolve=>{releaseContext=resolve;});
  const contextSeen=new Promise(resolve=>{contextStarted=resolve;});
  await page.route('**'+path,async route=>{
   if(route.request().method()!=='POST') {
    if(holdContext) {contextStarted();await contextGate;}
    if(scenario==='refresh-failure')return route.fulfill({status:503,body:'Current projection unavailable'});
    return route.continue();
   }
   posts++;
   const response=await route.fetch();
   if(!scenario.startsWith('submit-')) {assert.equal(response.status(),200);await route.abort('failed');}
   else await route.fulfill({response});
  });
  if(!scenario.startsWith('submit-')) {
   await form.getByRole('button',{name:'Record receipt',exact:true}).click();
   await page.getByText('Receipt status unknown. Check the original receipt before recording more money.',{exact:true}).waitFor();
  }
  assert.equal((await page.request.post(base+'/fixture/change-office-authority')).status(),200);
  // A real fresh server projection proves the independent grants changed while
  // this tab still holds the old HTML. Do not reload the browser under test.
  const live=await page.request.get(base+path);const liveHTML=await live.text();
  if(scenario.endsWith('customer'))assert.equal(live.status(),404);
  else {
   assert.equal(live.status(),200);
   if(scenario.endsWith('history'))assert(!liveHTML.includes('PRIVATE HISTORICAL REFERENCE'));
   if(scenario==='invoice')assert(!liveHTML.includes('173.29'));
  }
  holdContext=!scenario.startsWith('submit-');
  const responsePromise=page.waitForResponse(r=>new URL(r.url()).pathname===path&&r.request().method()===(scenario.startsWith('submit-')?'POST':'GET'));
  if(scenario.startsWith('submit-'))await form.getByRole('button',{name:'Record receipt',exact:true}).click();
  else await page.getByRole('button',{name:'Check original receipt',exact:true}).click();
  if(holdContext) {
   await contextSeen;
   const withheld=await page.content();
   for(const secret of ['PRIVATE CUSTOMER IDENTITY','PRIVATE HISTORICAL REFERENCE','173.29','PRIVATE LOCAL INPUT']) assert(!withheld.includes(secret),'stale content exposed while current context pending: '+secret);
   releaseContext();
  }
  await responsePromise;
  if(!scenario.endsWith('customer'))await page.getByText('Receipt accepted. Do not record this money again.',{exact:true}).waitFor();
  const html=await page.content();
  if(scenario.endsWith('history'))assert(!html.includes('PRIVATE HISTORICAL REFERENCE'),'revoked history retained');
  if(scenario==='invoice')assert(!html.includes('173.29'),'revoked Invoice due/options retained');
  if(scenario.endsWith('customer')||scenario==='refresh-failure') {
   for(const secret of ['PRIVATE CUSTOMER IDENTITY','PRIVATE HISTORICAL REFERENCE','173.29','PRIVATE LOCAL INPUT']) assert(!html.includes(secret),'invisible Customer retained '+secret);
  }
  if(scenario==='entry') {assert(html.includes('PRIVATE HISTORICAL REFERENCE'),'current independently authorized history should remain readable');assert.equal(await page.locator('form[data-office-payment]').count(),0);}
  assert.equal(posts,1,'refresh/reconciliation must not replay');
  assert.equal(await page.evaluate(()=>sessionStorage.getItem('freefsm.office-payment.pending-operation')),op);
  // Safe receipt ownership survives loss of sensitive Customer/Invoice/history
  // access. A rejected submission remains missing, never invented as accepted.
  const receipt=await page.request.get(base+'/payment-operations/'+op);
  assert.equal(receipt.status(),scenario==='submit-customer'?404:200);
  if(scenario!=='submit-customer') {
   await page.getByText('Receipt accepted. Do not record this money again.',{exact:true}).waitFor();
   assert.equal((await receipt.json()).data.operation_id,op);
  }
  console.log(scenario+': current projection withheld revoked data; one POST; safe original receipt independently reconciled');
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exit(1);});
