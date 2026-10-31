const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
const [base,cookie,lease,outgoing,incoming,today] = process.argv.slice(2);
const day = n => {const d=new Date(today+'T12:00:00Z');d.setUTCDate(d.getUTCDate()+n);return d.toISOString().slice(0,10);};
(async()=>{
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try {
  const context=await browser.newContext({viewport:{width:390,height:844}});
  await context.addCookies([{name:'session',value:cookie,url:base}]);
  const page=await context.newPage();
  const path=base+`/leases/${lease}/coverage`;
  const snapshot=async()=>await (await context.request.get(base+'/test-snapshot')).json();
  const open=async p=>assert.equal((await p.goto(path)).status(),200);
  const submit=async(form,name='Save coverage change')=>{const [r]=await Promise.all([form.page().waitForResponse(r=>r.url()===path && r.request().method()==='POST'),form.page().waitForNavigation(),form.getByRole('button',{name,exact:true}).click()]);return r.status()===303?200:r.status();};
  const create=p=>p.locator('form').filter({has:p.locator('input[name="action"][value="create"]')});
  const recovery=()=>page.locator('#coverage-recovery');
  const focus=async()=>{await page.waitForFunction(()=>document.activeElement?.hasAttribute('data-error-summary'));await page.locator('[data-error-summary] a[href="#coverage-effective_on"]').click();assert.equal(await page.locator('#coverage-effective_on').evaluate(e=>e===document.activeElement),true);};
  await open(page);
  let form=create(page);
  await form.locator('[name="kind"]').selectOption('remove');
  await form.locator('[name="effective_on"]').fill(day(10));
  await form.locator('[name="outgoing_id"]').fill(outgoing);
  await form.locator('[name="reason"]').fill('Private recovery proposal');
  const before=await snapshot();
  assert.equal(await submit(form),409);
  assert.match(await page.locator('[data-error-summary]').innerText(),/At least one Asset.*Replace/s);
  assert.equal(await recovery().locator('[name="reason"]').inputValue(),'Private recovery proposal');
  assert.equal(await recovery().locator('[name="outgoing_id"]').inputValue(),outgoing);
  assert.deepEqual(await snapshot(),before);await focus();
  form=recovery();await form.locator('[name="kind"]').selectOption('replace');await form.locator('[name="incoming_id"]').selectOption(incoming);
  await form.locator('[name="effective_on"]').fill(day(-1));
  assert.equal(await submit(form),409);
  assert.match(await page.locator('[data-error-summary]').innerText(),/in-force dates/);
  assert.equal(await recovery().locator('[name="effective_on"]').inputValue(),day(-1));assert.deepEqual(await snapshot(),before);await focus();
  form=recovery();await form.locator('[name="effective_on"]').fill(day(10));await form.locator('[name="rent_cents"]').fill('12.5');
  assert.equal(await submit(form),409);
  assert.match(await page.locator('[data-error-summary]').innerText(),/whole cents/);
  assert.equal(await recovery().locator('[name="rent_cents"]').inputValue(),'12.5');assert.deepEqual(await snapshot(),before);await focus();
  form=recovery();await form.locator('[name="rent_cents"]').fill('');
  const intent=await form.evaluate(f=>Object.fromEntries(new FormData(f)));
  assert.equal(await submit(form),200);
  const accepted=await snapshot();
  const replay=await context.request.post(path,{form:intent,headers:{Origin:base},maxRedirects:0});assert.equal(replay.status(),409);assert.equal((await replay.json()).disposition,'saved');assert.deepEqual(await snapshot(),accepted);
  // Two editors of the same pending event; the stale editor must compare and
  // explicitly revise before a second save can advance the timeline revision.
  const other=await context.newPage();await open(other);
  const event=p=>p.locator('section').filter({hasText:'Private recovery proposal'}).locator('form');
  form=event(other);await form.locator('[name="effective_on"]').fill(day(11));await form.locator('[name="reason"]').fill('Other editor revision');assert.equal(await submit(form),200);
  const changed=await snapshot();
  form=event(page);await form.locator('[name="effective_on"]').fill(day(12));await form.locator('[name="reason"]').fill('Reviewed stale proposal');assert.equal(await submit(form),409);
  assert.match(await page.locator('body').innerText(),/Other editor revision/);
  assert.equal(await recovery().locator('[name="effective_on"]').inputValue(),day(12));
  assert.equal(await recovery().getByRole('button',{name:'Save coverage change',exact:true}).count(),0);
  assert.equal(await recovery().getByRole('link',{name:'Discard proposed change'}).count(),1);
  assert.deepEqual(await snapshot(),changed);await focus();
  assert.equal(await submit(recovery(),'Revise against current coverage'),409);assert.deepEqual(await snapshot(),changed);
  assert.equal(await submit(recovery()),200);
  assert.match(await page.locator('body').innerText(),/Reviewed stale proposal/);
  // Discard a separate stale proposal and verify no write.
  form=other.locator('section').filter({hasText:'Other editor revision'}).locator('form');await form.locator('[name="reason"]').fill('Discard me');assert.equal(await submit(form),409);
  const discardBefore=await snapshot();other.on('dialog',d=>d.accept());
  await other.getByRole('link',{name:'Discard proposed change'}).click();
  await other.waitForFunction(()=>document.querySelector('#leave-dialog[open]') || !document.querySelector('#coverage-recovery'));
  if(await other.locator('#leave-dialog[open]').count()) await Promise.all([other.waitForNavigation(),other.getByRole('button',{name:'Discard changes',exact:true}).click()]);
  assert.deepEqual(await snapshot(),discardBefore);
  assert.equal(await other.locator('#coverage-recovery').count(),0);
  // A still-open authorized form must not be reflected after Billing View loss.
  await open(page);form=create(page);await form.locator('[name="kind"]').selectOption('add');await form.locator('[name="incoming_id"]').selectOption(outgoing);await form.locator('[name="effective_on"]').fill(day(20));await form.locator('[name="rent_cents"]').fill('999');await form.locator('[name="reason"]').fill('NEVER DISCLOSE THIS PROPOSAL');
  assert.equal((await context.request.post(base+'/test-revoke')).status(),200);
  const deniedBefore=await snapshot();
  const [denial]=await Promise.all([page.waitForResponse(r=>r.url()===path && r.request().method()==='POST'),form.getByRole('button',{name:'Save coverage change',exact:true}).click()]);assert.equal(denial.status(),403);
  await page.waitForFunction(()=>!document.querySelector('[name="rent_cents"]'));
  const denied=await page.content();assert.ok(!denied.includes('NEVER DISCLOSE'));assert.ok(!denied.includes('coverage-recovery'));assert.ok(!denied.includes('name="rent_cents"'));assert.deepEqual(await snapshot(),deniedBefore);
  console.log('PASS persisted last-Asset/date/amount rejection; retained fields and linked focus; correction and replay exactly once; stale compare/revise/discard; access-loss nondisclosure');
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
