const {chromium} = require('playwright-core');
const assert = require('node:assert/strict');
const path = require('node:path');
(async () => {
 const [base,session,job,asset,user,date] = process.argv.slice(2);
 const browser = await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try {
  const context = await browser.newContext({viewport:{width:390,height:844}});
  await context.addCookies([{name:'session',value:session,url:base}]);
  const page = await context.newPage();
  await page.goto(`${base}/jobs/${job}/assets/${asset}/service-entries`);
  const create=page.locator('#service-entry-create');
  await create.locator('[name=service_date]').fill(date);
  await create.locator('[name=summary]').fill('Browser replaced seal');
  await create.locator('[name=responsible_user_id]').selectOption(user);
  await create.getByRole('button',{name:'Record Service Entry',exact:true}).click();
  await page.getByText('Browser replaced seal',{exact:true}).first().waitFor();
  const revise=page.locator('form[data-service-revision]').first();
  await revise.locator('[name=summary]').fill('Browser inspected new seal');
  await revise.getByRole('button',{name:'Revise Service Entry',exact:true}).click();
  await page.getByText('Browser inspected new seal',{exact:true}).first().waitFor();
  await page.reload();
  assert.equal(await page.locator('article').getByText('Browser inspected new seal',{exact:true}).count(),1);
  assert.equal(await page.getByText('Browser replaced seal',{exact:true}).count(),0);
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
  if(process.env.SERVICE_ENTRY_EVIDENCE) await page.screenshot({path:path.join(process.env.SERVICE_ENTRY_EVIDENCE,'service-entry-phone.png'),fullPage:true});
  await context.close();
  const fresh=await browser.newContext();
  await fresh.addCookies([{name:'session',value:session,url:base}]);
  const reopened=await fresh.newPage();
  await reopened.goto(`${base}/assets/${asset}/service-entries`);
  await reopened.getByText('Browser inspected new seal',{exact:true}).waitFor();
  console.log('persisted browser create/revise/fresh office history PASS');
 } finally { await browser.close(); }
})().catch(e=>{console.error(e);process.exit(1)});
