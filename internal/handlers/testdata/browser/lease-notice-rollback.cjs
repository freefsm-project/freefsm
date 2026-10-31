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
  await page.locator('[name="action"]').selectOption('withdraw');
  await page.locator('[name="reason"]').fill('Retained browser withdrawal');
  const key=await page.locator('[name="operation_key"]').inputValue();
  const snapshot=async()=>await (await ctx.request.get(base+'/test-snapshot')).text();
  const before=await snapshot();
  const save=async()=>{const [r]=await Promise.all([page.waitForResponse(r=>r.url()===path&&r.request().method()==='POST'),page.waitForNavigation(),page.getByRole('button',{name:'Save notice change',exact:true}).click()]);return r.status();};
  assert.equal(await save(),409);
  assert.match(await page.locator('[data-error-summary]').innerText(),/transaction was rolled back/);
  assert.equal(await page.locator('[name="reason"]').inputValue(),'Retained browser withdrawal');
  assert.equal(await page.locator('[name="operation_key"]').inputValue(),key);
  assert.equal(await snapshot(),before);
  assert.equal((await ctx.request.post(base+'/test-repair')).status(),200);
  assert.equal(await save(),303);
  assert.match(await page.locator('body').innerText(),/No pending notice/);
  assert.match(await page.locator('body').innerText(),/Retained browser withdrawal/);
  console.log('PASS database-enforced overlap rollback, retained proposal and stable identity, explicit coverage repair and retry');
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1});
