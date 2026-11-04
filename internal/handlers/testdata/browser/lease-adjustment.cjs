const {chromium}=require('playwright-core');
const assert=require('node:assert/strict');
const [base,cookie,lease,asset,invoice]=process.argv.slice(2);
(async()=>{
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try{
  const ctx=await browser.newContext({viewport:{width:390,height:844}});
  await ctx.addCookies([{name:'session',value:cookie,url:base}]);
  const page=await ctx.newPage(),other=await ctx.newPage();
  const path=base+`/leases/${lease}/billing/adjustments`;
  const form=p=>p.locator('form').filter({has:p.locator('[name="operation_key"]')});
  const save=async(p,name='Apply adjustment')=>{const [r]=await Promise.all([p.waitForResponse(r=>r.url()===path+'?invoice_id='+invoice&&r.request().method()==='POST'),p.waitForNavigation(),form(p).getByRole('button',{name,exact:true}).click()]);return r.status();};
  await page.goto(base+`/leases/${lease}/billing`);
  await page.getByRole('link',{name:'Manual abatements, fees and damages',exact:true}).click();
  await page.getByLabel('Eligible Invoice').selectOption(invoice);
  await page.getByRole('button',{name:'Review selected Invoice',exact:true}).click();
  assert.equal((await other.goto(path+'?invoice_id='+invoice)).status(),200);
  for(const p of [page,other]){
   await form(p).getByLabel('Adjustment kind').selectOption('fee');
   await form(p).getByLabel('Lease Asset').selectOption(asset);
   await form(p).getByLabel('Explicit amount (USD cents)',{exact:true}).fill('25');
   await form(p).getByLabel('Outage / relevant start date').fill('2024-01-01');
   await form(p).getByLabel('Outage / relevant end date').fill('2024-01-01');
   await form(p).getByLabel('Reason',{exact:true}).fill(p===page?'Browser explicit fee':'Retained stale damages proposal');
  }
  await form(page).getByLabel('Outage / relevant end date').fill('2023-01-01');
  assert.equal(await save(page),409);
  assert.equal(await form(page).getByLabel('Reason',{exact:true}).inputValue(),'Browser explicit fee');
  await form(page).getByLabel('Outage / relevant end date').fill('2024-01-01');
  assert.equal(await save(page),303);
  assert.equal(await save(other),409);
  assert.match(await other.locator('body').innerText(),/Browser explicit fee/);
  assert.equal(await form(other).getByLabel('Reason',{exact:true}).inputValue(),'Retained stale damages proposal');
  assert.equal(await save(other,'Revise against current Invoice'),409);
  await form(other).getByLabel('Adjustment kind').selectOption('damages');
  assert.equal(await save(other),303);
  await form(other).getByLabel('Reason',{exact:true}).fill('Discard me');
  other.on('dialog',d=>d.accept());
  await other.getByRole('link',{name:'Discard proposed adjustment',exact:true}).click();
  await other.waitForFunction(()=>document.querySelector('#leave-dialog[open]')||!document.querySelector('[name="operation_key"]'));
  if(await other.locator('#leave-dialog[open]').count())await Promise.all([other.waitForNavigation(),other.getByRole('button',{name:'Discard changes',exact:true}).click()]);
  assert.equal(await form(other).count(),0);
  assert(await other.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'mobile page overflows');
  console.log('PASS authenticated mobile navigation, validation retention, compare/revise/discard, fee and damages history');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1});
