const {chromium}=require('playwright-core');
const assert=require('node:assert/strict');
const [base,cookie,lease,asset,start]=process.argv.slice(2);
(async()=>{
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try {
  const ctx=await browser.newContext({viewport:{width:390,height:844}});
  await ctx.addCookies([{name:'session',value:cookie,url:base}]);
  const page=await ctx.newPage(),other=await ctx.newPage();
  const path=base+`/leases/${lease}/billing/revisions`;
  const form=p=>p.locator('form').filter({has:p.locator('[name="operation_key"]')});
  const save=async(p,name='Save billing revision')=>{const [r]=await Promise.all([p.waitForResponse(r=>r.url()===path&&r.request().method()==='POST'),p.waitForNavigation(),form(p).getByRole('button',{name,exact:true}).click()]);return r.status();};
  assert.equal((await page.goto(base+`/leases/${lease}/billing`)).status(),200);
  await page.getByRole('link',{name:'Revise billing and view effective terms',exact:true}).click();
  assert.equal((await other.goto(path)).status(),200);
  for(const p of [page,other]){
   await form(p).locator(`[name="asset_id"][value="${asset}"]`).check();
   await form(p).locator('[name="effective_on"]').fill(start);
   await form(p).locator(`[name="rent_cents_${asset}"]`).fill('4700');
   await form(p).locator(`[name="tax_rate_${asset}"]`).fill('5');
   await form(p).locator('[name="reason"]').fill(p===page?'Accepted browser billing revision':'Retained stale billing revision');
  }
  await form(page).locator(`[name="tax_rate_${asset}"]`).fill('invalid');
  assert.equal(await save(page),409);
  assert.equal(await form(page).locator('[name="reason"]').inputValue(),'Accepted browser billing revision');
  await form(page).locator(`[name="tax_rate_${asset}"]`).fill('5');
  assert.equal(await save(page),303);
  assert.equal(await save(other),409);
  assert.match(await other.locator('body').innerText(),/Accepted browser billing revision/);
  assert.equal(await form(other).locator('[name="reason"]').inputValue(),'Retained stale billing revision');
  assert.equal(await form(other).getByRole('button',{name:'Save billing revision',exact:true}).count(),0);
  assert.equal(await save(other,'Revise against current terms'),409);
  assert.equal(await save(other),303);
  await form(page).locator(`[name="asset_id"][value="${asset}"]`).check();
  await form(page).locator('[name="effective_on"]').fill(start);
  await form(page).locator(`[name="rent_cents_${asset}"]`).fill('4800');
  await form(page).locator(`[name="tax_rate_${asset}"]`).fill('5');
  await form(page).locator('[name="reason"]').fill('Discard stale billing revision');
  assert.equal(await save(page),409);
  page.on('dialog',d=>d.accept());await page.getByRole('link',{name:'Discard proposed revision',exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('#leave-dialog[open]')||!document.querySelector('[data-error-summary]'));
  if(await page.locator('#leave-dialog[open]').count())await Promise.all([page.waitForNavigation(),page.getByRole('button',{name:'Discard changes',exact:true}).click()]);
  assert.equal(await form(page).locator('[name="reason"]').inputValue(),'');
  console.log('PASS mobile normal billing navigation, retained validation, stale comparison, explicit revise/discard, immutable history');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1});
