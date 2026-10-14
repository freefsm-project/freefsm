const assert = require('node:assert/strict');
const { randomUUID } = require('node:crypto');
const { chromium } = require('playwright-core');
const { JobExecutionReference } = require('../../../api/v1/reference/job-execution.js');
(async () => {
 const [base, cookie, job, token] = process.argv.slice(2);
 const browser = await chromium.launch({executablePath: process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium-browser',args:['--no-sandbox']});
 try {
  const context=await browser.newContext({viewport:{width:320,height:900}});
  await context.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
  const page=await context.newPage();
  await page.goto(`${base}/jobs/${job}/field-notes`);
  await page.locator('[name=field_notes]').fill('Browser persisted field observation');
  await page.getByRole('button',{name:'Save Field Notes',exact:true}).click();
  await page.waitForURL(/flash=/);
  await page.reload();
  assert.equal(await page.locator('[name=field_notes]').inputValue(),'Browser persisted field observation');
  assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
  const other=await context.newPage();
  await other.goto(`${base}/jobs/${job}/field-notes`);
  await other.locator('[name=field_notes]').fill('Concurrent browser accepted value');
  await other.getByRole('button',{name:'Save Field Notes',exact:true}).click();
  await other.waitForURL(/flash=/);
  await page.locator('[name=field_notes]').fill('Explicit browser reviewed revision');
  await page.getByRole('button',{name:'Save Field Notes',exact:true}).click();
  await page.getByRole('button',{name:'Compare current values and review',exact:true}).click();
  await page.getByText(/Current Field Notes:\nConcurrent browser accepted value/).waitFor();
  await page.getByRole('button',{name:'Save reviewed Field Notes',exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('[name=field_notes]')?.value==='Explicit browser reviewed revision' && !document.querySelector('[data-busy]'));
  await other.close();
  // Lose a real committed HTTP response; reconcile its original operation ID.
  await page.route(`**/jobs/${job}/field-notes`,async route=>{
   if(route.request().method()==='POST') {
    await route.continue({headers:{...await route.request().allHeaders(),'X-Test-Lose-Response':'once'}});
   }
   else await route.continue();
  });
  await page.locator('[name=field_notes]').fill('Lost response accepted once');
  await page.getByRole('button',{name:'Save Field Notes',exact:true}).click();
  await page.getByRole('button',{name:'Check original operation',exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('[name=field_notes]')?.value==='Lost response accepted once' && !document.querySelector('[data-busy]'));
  await page.unroute(`**/jobs/${job}/field-notes`);
  await page.goto(`${base}/jobs/${job}`);
  await page.getByLabel('Job Status',{exact:true}).selectOption({label:'Completed'});
  await page.getByRole('button',{name:'Save status',exact:true}).click();
  await page.waitForURL(/flash=Status/);
  const activity=await context.request.get(`${base}/jobs/${job}/activity`);
  assert((await activity.text()).includes('status_transitioned'),'Status lost its existing operational activity');
  await page.goto(`${base}/jobs/${job}/field-notes`);
  await page.locator('[name=field_notes]').fill('Completed remains actionable');
  await page.getByRole('button',{name:'Save Field Notes',exact:true}).click();
  await page.waitForURL(/flash=/);
  await page.getByRole('link',{name:'Field Notes revision history',exact:true}).click();
  await page.getByText('Lost response accepted once',{exact:true}).waitFor();
  assert.equal(await page.getByText('Lost response accepted once',{exact:true}).count(),1);
  // The executable offline client calls these same production HTTP routes.
  let loseResponse=false, calls=0;
  const call=async(path,method,body)=>{
   calls++;
   const response=await fetch(base+'/api/v1'+path,{method,headers:{Authorization:'Bearer '+token,'Content-Type':'application/json'},body:body?JSON.stringify(body):undefined});
   const parsed=await response.json();
   if(loseResponse && method==='POST'){loseResponse=false;throw new Error('simulated lost response');}
   return {status:response.status,data:parsed.data,error:parsed.error};
  };
  const reference=new JobExecutionReference(call,Date.now()+60000);
  const state=(await call(`/jobs/${job}/field-notes`,'GET')).data;
  const op=randomUUID();const before=calls;
  reference.stage(job,'field-notes',{notes_revision:state.notes_revision,field_notes:'Offline staged observation'},op);
  assert.equal(calls,before,'staging contacted server');
  loseResponse=true;
  await assert.rejects(reference.submit(op));
  const receipt=await reference.reconcile(op);assert.equal(receipt.code,'accepted');
  await assert.rejects(reference.submit(op),/review_required/);
  const rejected=randomUUID();reference.stage(job,'field-notes',{notes_revision:state.notes_revision,field_notes:'Stale offline overwrite'},rejected);
  assert.equal((await reference.submit(rejected)).status,409);
  assert.equal((await reference.reconcile(rejected)).code,'stale_notes_revision');
  assert.throws(()=>reference.repair(rejected,job,'field-notes',{notes_revision:state.notes_revision},rejected),/review_required/);
  const reviewed=(await call(`/jobs/${job}/field-notes`,'GET')).data;
  const repaired=randomUUID();reference.repair(rejected,job,'field-notes',{notes_revision:reviewed.notes_revision,field_notes:'Explicitly reviewed offline repair'},repaired);
  assert.equal((await reference.submit(repaired)).status,200);
  const choices=(await call(`/jobs/${job}/status-intents`,'GET')).data;
  const target=choices.find(c=>c.status.category==='job:pending');const statusOp=randomUUID();
  reference.stage(job,'status',target.intent,statusOp);assert.equal((await reference.submit(statusOp)).status,200);
  const history=(await call(`/jobs/${job}/field-notes/history`,'GET')).data;
  assert.equal(history.filter(h=>h.content==='Offline staged observation').length,1);
  reference.logout();assert.equal(reference.pending.size,0);
  assert.throws(()=>reference.stage(job,'status',target.intent,randomUUID()),/lease_expired/);
  await page.goto(`${base}/jobs/${job}/field-notes`);
  await page.locator('[name=field_notes]').fill('INACCESSIBLE_DRAFT_MUST_PURGE');
  await page.route(`**/jobs/${job}/field-notes`,async route=>{
   if(route.request().method()==='POST')await route.fulfill({status:403,body:'Forbidden'});
   else await route.continue();
  });
  await page.getByRole('button',{name:'Save Field Notes',exact:true}).click();
  await page.getByRole('link',{name:'Return to authorized Jobs',exact:true}).waitFor();
  assert(!await page.locator('body').textContent().then(text=>text.includes('INACCESSIBLE_DRAFT_MUST_PURGE')||text.includes('Explicitly reviewed offline repair')));
  assert.equal(await page.locator('textarea').count(),0);
  console.log('PASS 320px persisted notes/status/history, finished actionable, committed lost-response reconciliation, executable offline staging/receipt/reviewed repair');
 } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
