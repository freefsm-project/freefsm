'use strict';
const assert=require('node:assert/strict');
const {chromium}=require('playwright-core');
(async()=>{
 const {url,cookie}=JSON.parse(require('node:fs').readFileSync(0,'utf8'));
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE||'/usr/bin/chromium',headless:true,args:['--no-sandbox']});
 try{
  const context=await browser.newContext();await context.addCookies([{name:'session',value:cookie,url,httpOnly:true}]);const page=await context.newPage();
  assert.equal((await page.goto(url+'/settings')).status(),200);
  assert.equal(await page.locator('[name=offline_hours]').inputValue(),'24');
  const stale=await context.newPage();await stale.goto(url+'/settings');
  await page.locator('[name=offline_hours]').fill('72');
  await Promise.all([page.waitForNavigation(),page.getByRole('button',{name:'Save Offline',exact:true}).click()]);
  await page.reload();assert.equal(await page.locator('[name=offline_hours]').inputValue(),'72');
  await stale.locator('[name=offline_hours]').fill('48');
  const [conflict]=await Promise.all([stale.waitForResponse(r=>r.request().method()==='POST'),stale.getByRole('button',{name:'Save Offline',exact:true}).click()]);assert.equal(conflict.status(),409);
  await page.locator('[name=offline_hours]').fill('73');
  const [invalid]=await Promise.all([page.waitForResponse(r=>r.request().method()==='POST'),page.getByRole('button',{name:'Save Offline',exact:true}).click()]);assert.equal(invalid.status(),422);
  await page.goto(url+'/settings');assert.equal(await page.locator('[name=offline_hours]').inputValue(),'72');
  await page.locator('[name=offline_hours]').fill('0');await Promise.all([page.waitForNavigation(),page.getByRole('button',{name:'Save Offline',exact:true}).click()]);
  await page.reload();assert.equal(await page.locator('[name=offline_hours]').inputValue(),'0');
  assert.equal(await page.evaluate(async()=>('serviceWorker'in navigator?(await navigator.serviceWorker.getRegistrations()).length:0)),0);
  console.log('PASS persisted online-only Admin policy: default24, max72, >72 rejection, stale editor409, disable0; no website service worker');
 }finally{await browser.close();}
})().catch(e=>{console.error(e.message);process.exitCode=1;});
