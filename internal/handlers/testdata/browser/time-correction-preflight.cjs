const {chromium}=require('playwright-core');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
(async()=>{
 const [base,token,id]=process.argv.slice(2);
 const context=await chromium.launchPersistentContext(fs.mkdtempSync(path.join(process.env.TMPDIR,'tcp-')),{executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try {
  await context.addCookies([{name:'session',value:token,url:base}]);
  const page=await context.newPage();const ids=[];const entry='/time-entries/'+id;
  page.on('request',r=>{if(r.method()==='POST'&&new URL(r.url()).pathname===entry)ids.push(r.headers()['x-freefsm-action']||new URLSearchParams(r.postData()).get('save_action_id'));});
  await page.goto(base+entry+'/edit');
  await page.locator('[name=clock_in]').fill('not-a-timestamp');
  await page.locator('[name=notes]').fill('browser retained correction draft');
  await page.locator('[name=reason]').fill('browser reviewed correction reason');
  await Promise.all([page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname===entry),page.getByRole('button',{name:'Save',exact:true}).click()]);
  const receipt=await (await context.request.get(base+'/save-actions/'+ids[0])).json();
  assert.equal(receipt.disposition,'rejected');assert.equal(receipt.retry_safe,true);assert.equal(receipt.retry_same_operation??false,false);assert.equal(receipt.committed_steps,0);
  await page.locator('[data-action-state="rejected"]').waitFor();
  assert.equal(await page.locator('[name=clock_in]').inputValue(),'not-a-timestamp');
  assert.equal(await page.locator('[name=notes]').inputValue(),'browser retained correction draft');
  assert.equal(await page.locator('[name=reason]').inputValue(),'browser reviewed correction reason');
  const unchanged=await (await context.request.get(base+entry)).text();
  assert.match(unchanged,/browser original correction facts/);assert.doesNotMatch(unchanged,/browser retained correction draft/);
  await page.locator('[name=clock_in]').fill('2026-01-01T10:00:00Z');
  await page.getByRole('button',{name:'Save',exact:true}).click();
  await page.waitForURL(/\/time-entries\?/);
  assert.equal(ids.length,2);assert.ok(ids[0]);assert.notEqual(ids[0],ids[1]);
  const saved=await (await context.request.get(base+'/save-actions/'+ids[1])).json();
  assert.equal(saved.disposition,'saved');assert.equal(saved.committed_steps,1);
  await page.goto(base+entry);await page.reload();
  const history=await page.locator('[aria-label="Immutable time history"]').innerText();
  assert.match(history,/browser original correction facts/);assert.match(history,/browser retained correction draft/);assert.match(history,/browser reviewed correction reason/);
  console.log('Invalid correction rejected with zero committed steps; authorized timestamp/notes/reason retained; fresh-ID correction persisted once with immutable before/after history',JSON.stringify({ids,receipt,saved}));
 }finally{await context.close();}
})().catch(e=>{console.error(e);process.exit(1)});
