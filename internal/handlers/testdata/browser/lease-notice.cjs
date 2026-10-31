const {chromium}=require('playwright-core');
const assert=require('node:assert/strict');
const [base,cookie,lease,today]=process.argv.slice(2);
(async()=>{
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try {
  const ctx=await browser.newContext({viewport:{width:390,height:844}});
  await ctx.addCookies([{name:'session',value:cookie,url:base}]);
  const page=await ctx.newPage(),other=await ctx.newPage();
  const path=base+`/leases/${lease}/notice`;
  const open=async p=>assert.equal((await p.goto(path)).status(),200);
  const form=p=>p.locator('form').filter({has:p.locator('[name="operation_key"]')});
  const save=async(p,name='Save notice change')=>{const [r]=await Promise.all([p.waitForResponse(r=>r.url()===path&&r.request().method()==='POST'),p.waitForNavigation(),form(p).getByRole('button',{name,exact:true}).click()]);return r.status();};
  await open(page);await open(other);
  for(const p of [page,other]){await form(p).locator('[name="category"]').selectOption('customer_early');await form(p).locator('[name="effective_on"]').fill(today);await form(p).locator('[name="reason"]').fill(p===page?'Accepted browser notice':'Retained stale browser proposal');}
  assert.equal(await save(page),303);
  assert.equal(await save(other),409);
  assert.match(await other.locator('body').innerText(),/Accepted browser notice/);
  assert.equal(await form(other).locator('[name="reason"]').inputValue(),'Retained stale browser proposal');
  assert.equal(await form(other).getByRole('button',{name:'Save notice change',exact:true}).count(),0);
  assert.equal(await save(other,'Revise against current notice'),409);
  await form(other).locator('[name="action"]').selectOption('revise');
  assert.equal(await save(other),303);
  assert.match(await other.locator('body').innerText(),/Retained stale browser proposal/);
  await form(other).locator('[name="action"]').selectOption('withdraw');await form(other).locator('[name="reason"]').fill('Browser withdrawal');assert.equal(await save(other),303);
  assert.match(await other.locator('body').innerText(),/No pending notice/);
  assert.equal(await other.locator('[name="rent_cents"]').count(),0);
  const snapshot=async()=>await (await ctx.request.get(base+'/test-snapshot')).json();
  const before=await snapshot();
  await form(page).locator('[name="reason"]').fill('Discard this stale proposal');assert.equal(await save(page),409);
  page.on('dialog',d=>d.accept());await page.getByRole('link',{name:'Discard proposed change',exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('#leave-dialog[open]')||!document.querySelector('[data-error-summary]'));
  if(await page.locator('#leave-dialog[open]').count())await Promise.all([page.waitForNavigation(),page.getByRole('button',{name:'Discard changes',exact:true}).click()]);
  assert.equal(await snapshot(),before);
  await open(page);await form(page).locator('[name="category"]').selectOption('customer_early');await form(page).locator('[name="effective_on"]').fill(today);await form(page).locator('[name="reason"]').fill('NEVER REFLECT REVOKED PROPOSAL');
  assert.equal((await ctx.request.post(base+'/test-revoke')).status(),200);
  const [denial]=await Promise.all([page.waitForResponse(r=>r.url()===path&&r.request().method()==='POST'),form(page).getByRole('button',{name:'Save notice change',exact:true}).click()]);assert.equal(denial.status(),403);
  assert.ok(!(await denial.text()).includes('NEVER REFLECT'));assert.equal(await snapshot(),before);
  console.log('PASS base-authorized notice create, stale compare/revise/discard, withdrawal, retained history, billing isolation, authority-loss nondisclosure');
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1});
