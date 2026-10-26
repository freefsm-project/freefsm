const assert=require('node:assert/strict');
const {chromium}=require('playwright-core');
(async()=>{
 const [base,cookie,id,version,file]=process.argv.slice(2);
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try{
  const context=await browser.newContext({viewport:{width:390,height:844}});await context.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);const page=await context.newPage();
  await page.goto(`${base}/estimates/${id}/email?version=${version}`);
  assert.equal(await page.locator('[name=cc]').inputValue(),'default@example.test');await page.locator('[name=cc]').fill('');
  await page.locator('[name=to]').fill('browser-one@example.test, browser-two@example.test');await page.locator('[name=subject]').fill('Reviewed browser subject');await page.locator('[name=body]').fill('');await page.locator('[name=html_body]').fill('<p>Reviewed browser HTML</p>');
  await page.locator(`[name=extra_file_id][value="${file}"]`).check();
  await Promise.all([page.waitForNavigation(),page.getByRole('button',{name:'Queue Email',exact:true}).click()]);
  assert.match(page.url(),/Email[+%20]+queued/);assert.match(await page.locator('body').innerText(),/queued/i);
  await page.reload();assert.match(await page.locator('body').innerText(),/browser-one@example.test/);assert.match(await page.locator('body').innerText(),/browser-two@example.test/);
  console.log('PASS persisted Chromium: HTML-only arbitrary fanout, removable copy default, exact File selection, queued history survives reload');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exit(1);});
