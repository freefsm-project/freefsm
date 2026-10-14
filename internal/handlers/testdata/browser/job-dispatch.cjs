const {chromium}=require('playwright-core');
const assert=require('node:assert/strict');
(async()=>{
 const [base,session,job,worker]=process.argv.slice(2);
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE||'/usr/bin/chromium-browser',args:['--no-sandbox']});
 try {
  const context=await browser.newContext({viewport:{width:1280,height:900}});
  await context.addCookies([{name:'session',value:session,url:base}]);
  const page=await context.newPage();const path=`/jobs/${job}/dispatch`;
  await page.goto(base+'/schedule?tab=calendar&period=month&year=2026&month=10');
  await page.locator(`[data-job-id="${job}"]`).first().dragTo(page.locator('td.calendar-drop-slot[data-date="2026-10-21"]'));
  await page.waitForURL(/\/dispatch\?/);
  assert(await page.getByRole('dialog').isVisible());
  assert.match(await page.locator('[name=start]').inputValue(),/^2026-10-21T09:15/);
  assert.match(await page.locator('[name=end]').inputValue(),/^2026-10-21T11:45/);
  assert.match(await page.locator('[name=arrival_start]').inputValue(),/^2026-10-21T09:00/);
  await page.getByRole('link',{name:'Cancel',exact:true}).click();
  await page.goto(base+path);
  assert.match(await page.locator('[name=start]').inputValue(),/^2026-10-20T09:15/);
  await page.setViewportSize({width:320,height:900});
  async function proposal() {
   await page.locator('[name=start]').fill('2026-10-21T23:15');
   await page.locator('[name=end]').fill('2026-10-22T01:45');
   await page.locator('[name=arrival_start]').fill('2026-10-21T23:00');
   await page.locator('[name=arrival_end]').fill('2026-10-21T23:30');
   await page.locator(`[name=assignee_ids][value="${worker}"]`).check();
   const [response]=await Promise.all([page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname===path),page.getByRole('button',{name:'Review dispatch',exact:true}).click()]);
   assert.equal(response.status(),200);
   await page.getByRole('button',{name:'Confirm dispatch',exact:true}).waitFor();
   assert(await page.getByText('Browser conflicting Job',{exact:false}).isVisible());
   assert(await page.getByText('Browser equal worker',{exact:false}).isVisible());
  }
  await proposal();
  await page.getByRole('link',{name:'Cancel',exact:true}).click();
  await page.goto(base+path);
  assert.match(await page.locator('[name=start]').inputValue(),/^2026-10-20T09:15/);
  await proposal();
  // A separate real browser editor changes current dispatch after this review.
  const other=await context.newPage();await other.goto(base+path);
  await other.locator('[name=end]').fill('2026-10-20T12:00');
  await other.getByRole('button',{name:'Review dispatch',exact:true}).click();
  await other.getByRole('button',{name:'Confirm dispatch',exact:true}).click();
  await other.waitForURL(new RegExp(`/jobs/${job}(\\?|$)`));await other.close();
  await page.locator('[name=acknowledge_overlaps]').check();
  const [conflict]=await Promise.all([page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname===path),page.getByRole('button',{name:'Confirm dispatch',exact:true}).click()]);
  assert.equal(conflict.status(),409);
  await page.getByRole('region',{name:'Current persisted dispatch'}).waitFor();
  assert.match(await page.locator('[name=end]').inputValue(),/^2026-10-22T01:45/);
  assert(await page.locator(`[name=assignee_ids][value="${worker}"]`).isChecked());
  await proposal(); // Explicitly review the preserved proposal against current state.
  await page.locator('[name=acknowledge_overlaps]').check();
  // Lose the response only after the real server has durably committed. The
  // existing online save receipt must reconcile the same review identity.
  let lost=false;
  await page.route('**'+path,async route=>{
   if(route.request().method()==='POST'&&!lost&&route.request().postData().includes('review_id')){
    lost=true;
    const headers=await route.request().allHeaders();
    await route.continue({headers:{...headers,'X-Test-Lose-Response':'true'}});
   }else await route.continue();
  });
  await page.getByRole('button',{name:'Confirm dispatch',exact:true}).click();
  await page.waitForFunction(()=>JSON.parse(sessionStorage.getItem('freefsm-unresolved-actions')||'[]').length===0,null,{timeout:15000});
  await page.goto(base+path);assert(lost);
  assert.match(await page.locator('[name=start]').inputValue(),/^2026-10-21T23:15/);
  assert.match(await page.locator('[name=end]').inputValue(),/^2026-10-22T01:45/);
  assert(await page.locator(`[name=assignee_ids][value="${worker}"]`).isChecked());
  await page.locator('[name=end]').fill('2026-10-20T01:45');
  const [bad]=await Promise.all([page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname===path),page.getByRole('button',{name:'Review dispatch',exact:true}).click()]);
  assert.equal(bad.status(),422);await page.locator('[data-error-summary]').waitFor();
  assert.match(await page.locator('[name=end]').inputValue(),/^2026-10-20T01:45/);
  assert(await page.evaluate(()=>document.activeElement.matches('[data-error-summary][role=alert]')));
  assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
  if(process.env.DISPATCH_SCREENSHOT)await page.screenshot({path:process.env.DISPATCH_SCREENSHOT,fullPage:true});
  await page.keyboard.press('Escape');
  await page.getByRole('button',{name:'Discard changes',exact:true}).click();
  await page.waitForURL(new RegExp(`/jobs/${job}$`));
  console.log(JSON.stringify({dragProposal:true,cancelUnchanged:true,fullInterval:true,equalAssignees:true,overlapReviewed:true,staleReviewCompared:true,proposalAssigneesRetained:true,lostResponseReconciled:true,validationRetained:true,keyboardEscape:true,phoneWidth:320}));
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exit(1)});
