const {chromium}=require('playwright-core');
const assert=require('node:assert/strict');
const [base,cookie,lease]=process.argv.slice(2);
(async()=>{
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try {
  const ctx=await browser.newContext();
  await ctx.addCookies([{name:'session',value:cookie,url:base}]);
  const page=await ctx.newPage(),path=base+`/leases/${lease}/notice`;
  assert.equal((await page.goto(path)).status(),200);
  await page.locator('[name="reason"]').fill('Authorized ended proposal');
  const date=await page.locator('[name="notice_on"]').inputValue();
  assert.equal((await ctx.request.post(base+'/test-end')).status(),200);
  const snapshot=async()=>await (await ctx.request.get(base+'/test-snapshot')).text();
  const before=await snapshot();
  const [response]=await Promise.all([page.waitForResponse(r=>r.url()===path&&r.request().method()==='POST'),page.getByRole('button',{name:'Save notice change',exact:true}).click()]);
  assert.equal(response.status(),409);
  await page.locator('[data-error-summary]').waitFor();
  assert.match(await page.locator('[data-error-summary]').innerText(),/This Lease has ended/);
  assert.equal(await page.locator('[name="reason"]').inputValue(),'Authorized ended proposal');
  assert.equal(await page.locator('[name="notice_on"]').inputValue(),date);
  assert.equal(await page.locator('[name="reason"]').isDisabled(),true);
  assert.equal(await page.getByRole('button',{name:'Save notice change',exact:true}).count(),0);
  assert.equal(await page.getByRole('button',{name:'Revise against current notice',exact:true}).count(),0);
  assert.match(await page.locator('body').innerText(),/Current saved facts/);
  assert.equal(await snapshot(),before);
  page.on('dialog',d=>d.accept());
  await page.getByRole('link',{name:'Discard proposed change',exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('#leave-dialog[open]')||!document.querySelector('[data-error-summary]'));
  if(await page.locator('#leave-dialog[open]').count())await page.getByRole('button',{name:'Discard changes',exact:true}).click();
  await page.waitForFunction(()=>!document.querySelector('[data-error-summary]'));
  assert.equal(await snapshot(),before);
  console.log('PASS ended authorized read-only proposal, persistent explanation, dates, comparison, discard, unchanged durable effects');
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1});
