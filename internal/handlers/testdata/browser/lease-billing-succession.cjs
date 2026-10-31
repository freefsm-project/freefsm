const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
const [base, cookie, lease, event] = process.argv.slice(2);
(async () => {
 const browser = await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try {
  const context = await browser.newContext({viewport:{width:390,height:844}});
  await context.addCookies([{name:'session',value:cookie,url:base}]);
  const page = await context.newPage();
  const path = base+`/leases/${lease}/coverage`;
  async function propose() {
   assert.equal((await page.goto(path)).status(),200);
   const form=page.locator('section form').filter({has:page.locator(`input[name="event_id"][value="${event}"]`)});
   await form.locator('[name="effective_on"]').fill('2024-03-10');
   await form.locator('[name="reason"]').fill('Retained browser allocation correction');
   const [response]=await Promise.all([page.waitForResponse(r=>r.url()===path && r.request().method()==='POST'),page.waitForNavigation(),form.getByRole('button',{name:'Save coverage change'}).click()]);
   assert.equal(response.status(),409);
   await page.locator('#coverage-recovery').waitFor();
   assert.match(await page.getByRole('alert').innerText(),/approved billing revision.*Asset.*2024-03-01/);
   assert.equal(await page.locator('#coverage-reason').inputValue(),'Retained browser allocation correction');
   assert.equal(await page.locator('#coverage-effective_on').inputValue(),'2024-03-10');
  }
  await propose();
  page.on('dialog',d=>d.accept());
  await page.getByRole('link',{name:'Discard proposed change'}).click();
  await page.waitForFunction(()=>document.querySelector('#leave-dialog[open]')||!document.querySelector('#coverage-recovery'));
  if(await page.locator('#leave-dialog[open]').count())await Promise.all([page.waitForNavigation(),page.getByRole('button',{name:'Discard changes',exact:true}).click()]);
  assert.equal(await page.locator('#coverage-recovery').count(),0);
  await propose();
  await page.locator('#coverage-effective_on').fill('2024-02-28');
  await Promise.all([page.waitForNavigation(),page.locator('#coverage-recovery').getByRole('button',{name:'Save coverage change'}).click()]);
  assert.equal(await page.locator('#coverage-recovery').count(),0);
  assert.match(await page.locator('body').innerText(),/2024-02-28/);
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
  console.log('PASS phone coverage/billing allocation rejection, retained proposal, discard and corrected-date save');
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
