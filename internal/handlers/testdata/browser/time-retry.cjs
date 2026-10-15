const {chromium}=require('playwright-core');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
(async()=>{
 const [base,token,id]=process.argv.slice(2);
 const context=await chromium.launchPersistentContext(fs.mkdtempSync(path.join(process.env.TMPDIR,'tr-')),{executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try {
  await context.addCookies([{name:'session',value:token,url:base}]);
  const page=await context.newPage();const operations=[];
  page.on('request',r=>{if(r.method()==='POST'&&new URL(r.url()).pathname==='/time-entries/'+id){operations.push(r.headers()['x-freefsm-action']||new URLSearchParams(r.postData()).get('save_action_id'));}});
  await page.goto(base+'/time-entries/'+id+'/edit');
  await page.locator('[name=notes]').fill('after browser dependency recovery');
  await page.locator('[name=reason]').fill('Browser exact-identity recovery');
  await page.getByRole('button',{name:'Save',exact:true}).click();
  const retry=page.getByRole('button',{name:'Retry same action',exact:true});
  await retry.waitFor({timeout:5000}).catch(async error=>{console.error('Retry state',await page.locator('body').innerText(),operations.length ? await (await context.request.get(base+'/save-actions/'+operations[0])).text() : 'no POST');throw error;});
  assert.match(await page.locator('body').innerText(),/Nothing was committed/);
  assert.equal((await context.request.get(base+'/__restore_history')).status(),204);
  await retry.click();
  await page.waitForURL(url=>url.pathname==='/time-entries');
  await page.goto(base+'/time-entries/'+id);
  const history=await page.locator('[aria-label="Immutable time history"]').innerText();
  assert.match(history,/before browser dependency fault/);assert.match(history,/after browser dependency recovery/);
  assert.equal(history.split('Browser exact-identity recovery').length-1,1);
  assert.equal(operations.length,2);assert.ok(operations[0]);assert.equal(operations[0],operations[1]);
  console.log('Real history failure → verified nonterminal receipt → explicit same-ID browser retry → one persisted correction passed');
 } finally {await context.close();}
})().catch(e=>{console.error(e);process.exit(1)});
