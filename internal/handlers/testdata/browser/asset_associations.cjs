const assert = require('node:assert/strict');
const {chromium} = require('playwright-core');
(async()=>{
 const [base,cookie,asset,customer,destination,job]=process.argv.slice(2);
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try {
  const context=await browser.newContext({viewport:{width:390,height:844}});
  await context.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
  const page=await context.newPage();
  for(const [action,date,reason] of [['place','2020-01-01','Browser first placement'],['return','2020-02-01','Browser return'],['place','2020-03-01','Browser revisit'],['correct','2020-01-02','Browser factual correction']]){
   await page.goto(`${base}/assets/${asset}/associations`);
   const form=page.locator(`form[action$="/${action}"]`);
   await form.locator('..').locator('summary').click();
   if(action!=='return'){
    await form.locator('[name=customer_id]').selectOption(customer);
    await form.locator('[name=location_id] option').filter({hasText:'Browser placement site'}).waitFor({state:'attached'});
    await form.locator('[name=location_id]').selectOption({label:'Browser placement site'});
   }
   if(action==='correct')await form.locator('[name=event_id]').selectOption({index:0});
   await form.locator('[name=effective_date]').fill(date);
   await form.locator('[name=reason]').fill(reason);
   await Promise.all([page.waitForNavigation(),form.getByRole('button',{name:`Save ${action}`}).click()]);
   assert.equal(new URL(page.url()).pathname,`/assets/${asset}/associations`);
   await page.reload();
   assert.ok((await page.locator('main').innerText()).includes(reason));
  }
  await page.goto(`${base}/customers/${customer}/asset-history`);
  const text=await page.locator('main').innerText();
  for(const value of ['BROWSER-035','2020-01-02','2020-03-01'])assert.ok(text.includes(value),value);
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
  if(process.env.ASSET_ASSOCIATION_EVIDENCE)await page.screenshot({path:process.env.ASSET_ASSOCIATION_EVIDENCE+'/customer-periods-phone.png',fullPage:true});
  await page.goto(`${base}/jobs/${job}/assets`);
  await page.locator('[name=asset_id]').selectOption(asset);
  await Promise.all([page.waitForNavigation(),page.getByRole('button',{name:'Link Asset',exact:true}).click()]);
  await page.goto(`${base}/assets/${asset}/associations`);
  const transfer=page.locator('form[action$="/transfer"]');
  await transfer.locator('..').locator('summary').click();
  await transfer.locator('[name=customer_id]').selectOption(destination);
  await transfer.locator('[name=effective_date]').fill(new Date().toLocaleDateString('en-CA',{timeZone:'UTC'}));
  await transfer.locator('[name=reason]').fill('Browser transfer preserves Job');
  await Promise.all([page.waitForNavigation(),transfer.getByRole('button',{name:'Save transfer'}).click()]);
  await page.goto(`${base}/jobs/${job}`);
  assert.ok((await page.locator('main').innerText()).includes('Historical (non-authorizing)'));
  assert.ok(!(await page.locator('main').innerText()).includes('Browser destination Customer'));
  await page.goto(`${base}/jobs/${job}/assets`);
  await Promise.all([page.waitForNavigation(),page.getByRole('button',{name:'Unlink BROWSER-035'}).click()]);
  assert.ok((await page.locator('main').innerText()).includes('explicitly unlinked'));
  const fresh=await browser.newContext();await fresh.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
  const check=await fresh.newPage();await check.goto(`${base}/assets/${asset}/associations`);
  const persisted=await check.locator('main').innerText();
  for(const value of ['Browser first placement','Browser return','Browser revisit','Browser factual correction','2020-01-01','2020-01-02'])assert.ok(persisted.includes(value),value);
  console.log('Phone placement, matching Location, return, revisit, correction, explicit Job link, transfer and historical unlink persisted; fresh context retained immutable evidence and distinct Customer periods.');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
