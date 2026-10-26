const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const { randomUUID } = require('node:crypto');
const { chromium } = require('playwright-core');
(async () => {
 const [base, cookie, id, token, generatorCookie, readerCookie] = process.argv.slice(2);
 const browser = await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try {
  const context=await browser.newContext({viewport:{width:390,height:844}});
  await context.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
  let page=await context.newPage(); const path=`/invoices/${id}/pdf`;
  await page.goto(base+path+'/preview');
  let completed;
  const accepted=new Promise(resolve=>{completed=resolve;});
  await page.route('**'+path+'/save',async route=>{const response=await route.fetch({maxRedirects:0});assert.equal(response.status(),303);await route.abort('failed');completed();});
  await page.getByRole('button',{name:'Generate new PDF version'}).click();await accepted;
  await page.unroute('**'+path+'/save');
  await page.close(); page=await context.newPage();
  // Reconcile the saved versions after uncertain navigation; no automatic POST.
  await page.goto(base+path+'/preview');
  assert.equal(await page.locator('input[name=version]').count(),1);
  const old=await page.locator('input[name=version]').inputValue();
  const original=await (await context.request.get(base+path)).body();
  const headers={Authorization:'Bearer '+token};
  const source=(await (await context.request.get(base+`/api/v1/invoices/${id}`,{headers})).json()).data;
  assert.equal((await context.request.patch(base+`/api/v1/invoices/${id}`,{headers,data:{operation_id:randomUUID(),job_id:source.job_id,revision:source.revision,notes:'Browser rendered change'}})).status(),200);
  await page.reload();assert.match(await page.locator('body').innerText(),/This PDF is stale/);
  const selected=page.locator('form').filter({has:page.locator(`input[name=version][value="${old}"]`)});
  assert.equal(await selected.locator('input[name=confirm_stale]').isChecked(),false);
  assert.equal(await selected.evaluate(form=>form.checkValidity()),false);
  await selected.locator('input[name=confirm_stale]').check();
  const downloadPromise=page.waitForEvent('download');await selected.getByRole('button',{name:'Download selected PDF'}).click();
  const download=await downloadPromise;assert.deepEqual(await fs.readFile(await download.path()),original);
  await Promise.all([page.waitForNavigation(),page.getByRole('button',{name:'Generate new PDF version'}).click()]);
  assert.equal(await page.locator('input[name=version]').count(),2);
  assert.notEqual(await page.locator('input[name=version]').first().inputValue(),old);
  assert.notDeepEqual(await (await context.request.get(base+path)).body(),original);
  for (const [value,generate,download] of [[generatorCookie,1,0],[readerCookie,0,2]]) {
   const independent=await browser.newContext();await independent.addCookies([{name:'session',value,url:base,httpOnly:true}]);
   const tab=await independent.newPage();await tab.goto(base+path+'/preview');
   assert.equal(await tab.getByRole('button',{name:'Generate new PDF version'}).count(),generate);
   assert.equal(await tab.getByRole('button',{name:'Download selected PDF'}).count(),download);
   await independent.close();
  }
  console.log('PASS persisted Chromium: lost response reconciled, exact stale selection, regenerated identity/default, independent grants');
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exit(1);});
