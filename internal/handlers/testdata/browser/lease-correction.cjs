const {chromium}=require('playwright-core');
const assert=require('node:assert/strict');
const [base,cookie,lease]=process.argv.slice(2);
(async()=>{
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try {
  const ctx=await browser.newContext({viewport:{width:390,height:844}});
  await ctx.addCookies([{name:'session',value:cookie,url:base}]);
  const page=await ctx.newPage(),other=await ctx.newPage();
  const path=base+`/leases/${lease}/correction`;
  const form=p=>p.locator('form').filter({has:p.locator('[name="operation_key"]')});
  const save=async(p,name='Save factual correction')=>{const [r]=await Promise.all([p.waitForResponse(r=>r.url()===path&&r.request().method()==='POST'),p.waitForNavigation(),form(p).getByRole('button',{name,exact:true}).click()]);return r.status();};
  assert.equal((await page.goto(base+`/leases/${lease}`)).status(),200);
  await page.getByRole('link',{name:'Correct agreement facts',exact:true}).click();
  assert.equal((await other.goto(path)).status(),200);
  for(const p of [page,other]){await form(p).locator('[name="factual"]').check();await form(p).locator('[name="reason"]').fill(p===page?'Accepted browser correction':'Retained stale correction');}
  assert.equal(await save(page),303);
  assert.equal(await save(other),409);
  assert.match(await other.locator('body').innerText(),/Accepted browser correction/);
  assert.equal(await form(other).locator('[name="reason"]').inputValue(),'Retained stale correction');
  assert.equal(await form(other).getByRole('button',{name:'Save factual correction',exact:true}).count(),0);
  assert.equal(await save(other,'Revise against current facts'),409);
  assert.equal(await save(other),303);
  await form(page).locator('[name="reason"]').fill('Discard stale correction');
  await form(page).locator('[name="factual"]').check();
  assert.equal(await save(page),409);
  page.on('dialog',d=>d.accept());await page.getByRole('link',{name:'Discard proposed correction',exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('#leave-dialog[open]')||!document.querySelector('[data-error-summary]'));
  if(await page.locator('#leave-dialog[open]').count())await Promise.all([page.waitForNavigation(),page.getByRole('button',{name:'Discard changes',exact:true}).click()]);
  assert.equal(await form(page).locator('[name="reason"]').inputValue(),'');
  assert.equal(await page.locator('[name="rent_cents"]').count(),0);
  console.log('PASS normal navigation, mobile factual correction, stale comparison, explicit revise and discard, retained proposal, history');
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1});
