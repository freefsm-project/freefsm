const {chromium}=require('playwright-core');
const assert=require('node:assert/strict');
(async()=>{
 const [base,session,customer,first,second]=process.argv.slice(2);
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE||'/usr/bin/chromium',args:['--no-sandbox']});
 try {
  const context=await browser.newContext({viewport:{width:320,height:900}});
  await context.addCookies([{name:'session',value:session,url:base}]);
  const page=await context.newPage();
  async function save(name,path) {
   const [response]=await Promise.all([
    page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname===path),
    page.waitForNavigation({waitUntil:'networkidle'}),
    page.getByRole('button',{name,exact:true}).click()
   ]);
   assert.equal(response.status(),303);
   await response.finished();
   // A repeated save redirects to the same URL. URL/network load-state waits
   // may already be satisfied before receipt reconciliation finishes; wait for
   // the real client to confirm it before deliberately reloading the page.
   await page.waitForFunction(()=>JSON.parse(sessionStorage.getItem('freefsm-unresolved-actions')||'[]').length===0,null,{timeout:10000});
   await page.waitForLoadState('networkidle');
  }
  await page.goto(base+'/jobs/new');
  await page.locator('[name=customer_id]').selectOption(customer);
  await page.locator('[name=job_type]').fill('Browser minimal Job');
   assert.equal(await page.locator('[name=start_time]').count(),0);
  await save('Save','/jobs');
  await page.waitForURL(/\/jobs\?/);
  await page.getByRole('link',{name:'Browser minimal Job',exact:true}).first().click();
  await page.waitForURL(/\/jobs\/\d+$/);
  const path=new URL(page.url()).pathname;
  await page.getByRole('link',{name:'Manage relationships',exact:true}).click();
  await page.getByRole('link',{name:'Manage explicit Job Contacts',exact:true}).click();
  await page.locator(`[name=contact_ids][value="${first}"]`).check();
  await page.locator(`[name=contact_ids][value="${second}"]`).check();
  await save('Save Contacts',path+'/contacts');
  await page.waitForURL(/contacts\?/);await page.reload();await page.waitForLoadState('networkidle');
  assert(await page.locator(`[name=contact_ids][value="${first}"]`).isChecked());
  assert(await page.locator(`[name=contact_ids][value="${second}"]`).isChecked());
  await page.locator(`[name=contact_ids][value="${first}"]`).uncheck();
  await save('Save Contacts',path+'/contacts');
  await page.waitForURL(/contacts\?/);await page.reload();await page.waitForLoadState('networkidle');
  assert(!(await page.locator(`[name=contact_ids][value="${first}"]`).isChecked()));
  assert(await page.locator(`[name=contact_ids][value="${second}"]`).isChecked());
  await page.waitForFunction(()=>!document.body.textContent.includes('An earlier action still needs reconciliation'),null,{timeout:5000});
  assert(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth));
  if(process.env.JOB_SCREENSHOT)await page.screenshot({path:process.env.JOB_SCREENSHOT,fullPage:true});
  console.log(JSON.stringify({job:path,minimal:true,multipleContacts:true,persisted:true,phoneWidth:320}));
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exit(1)});
