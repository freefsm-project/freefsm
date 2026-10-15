const {chromium}=require('playwright-core');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
(async()=>{
 const [base,token]=process.argv.slice(2);
 const context=await chromium.launchPersistentContext(fs.mkdtempSync(path.join(process.env.TMPDIR,'tp-')),{executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try {
  await context.addCookies([{name:'session',value:token,url:base}]);
  const page=await context.newPage();const ids=[];
  page.on('request',r=>{if(r.method()==='POST'&&new URL(r.url()).pathname==='/time-entries')ids.push(r.headers()['x-freefsm-action']||new URLSearchParams(r.postData()).get('save_action_id'));});
  await page.goto(base+'/time-entries/new');
  await page.locator('[name=clock_in]').fill('not-a-timestamp');
  await page.locator('[name=clock_out]').fill('2026-01-01T11:00:00Z');
  await page.locator('[name=notes]').fill('browser retained manual draft');
  await Promise.all([page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname==='/time-entries'),page.getByRole('button',{name:'Create closed entry',exact:true}).click()]);
  const receipt=await (await context.request.get(base+'/save-actions/'+ids[0])).json();
  assert.equal(receipt.disposition,'rejected');assert.equal(receipt.retry_safe,true);assert.equal(receipt.committed_steps,0);
  await page.locator('[data-action-state="rejected"]').waitFor();
  assert.equal(await page.locator('[name=notes]').inputValue(),'browser retained manual draft');
  await page.locator('[name=clock_in]').fill('2026-01-01T10:00:00Z');
  await page.getByRole('button',{name:'Create closed entry',exact:true}).click();
  await page.waitForURL(/\/time-entries\/\d+$/);await page.reload();
  assert.match(await page.locator('body').innerText(),/browser retained manual draft/);
  assert.equal(ids.length,2);assert.ok(ids[0]);assert.notEqual(ids[0],ids[1]);
  console.log('Malformed manual time is known rejected; editable draft retained; corrected fresh identity creates once');
 }finally{await context.close();}
})().catch(e=>{console.error(e);process.exit(1)});
