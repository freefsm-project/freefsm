const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
(async () => {
 const [base, admin, reviewer, location] = process.argv.slice(2);
 const browser = await chromium.launch({headless:true, executablePath:process.env.BROWSER_EXECUTABLE, args:['--no-sandbox']});
 try {
  const context=await browser.newContext();
  await context.addCookies([{name:'session',value:admin,url:base}]);
  const page=await context.newPage();
  await page.goto(base+'/settings/mail');
  await page.locator('[name=smtp_host]').fill('smtp.browser.example');
  await page.locator('[name=smtp_port]').fill('587');
  await page.locator('[name=smtp_from]').fill('browser@example.com');
  await page.locator('[name=smtp_password]').fill('browser-secret-canary');
  await page.getByRole('button',{name:'Save settings',exact:true}).click();
  await page.waitForURL(/flash=Settings/);
  await page.reload();
  assert.equal(await page.locator('[name=smtp_host]').inputValue(),'smtp.browser.example');
  assert.equal(await page.locator('[name=smtp_password]').inputValue(),'');
  assert.ok(!(await page.content()).includes('browser-secret-canary'));
  await page.goto(base+'/settings/maps');
  await page.locator('[name=map_tile_url]').fill('https://tiles.example/{z}/{x}/{y}.png');
  await page.getByRole('button',{name:'Save settings',exact:true}).click();
  await page.waitForURL(/flash=Settings/);await page.reload();
  assert.equal(await page.locator('[name=map_tile_url]').inputValue(),'https://tiles.example/{z}/{x}/{y}.png');
  await page.goto(`${base}/locations/${location}/coordinates`);
  await page.locator('[name=latitude]').fill('45.1');await page.locator('[name=longitude]').fill('-70.2');
  await page.getByRole('button',{name:'Accept coordinates'}).click();
  await page.waitForURL(/flash=Coordinates/);await page.reload();
  assert.equal(await page.locator('[name=latitude]').inputValue(),'45.1');
  const read=await browser.newContext();await read.addCookies([{name:'session',value:reviewer,url:base}]);
  const view=await read.newPage();assert.equal((await view.goto(base+'/settings/mail')).status(),200);
  assert.equal(await view.locator('[name=smtp_host]').isDisabled(),true);
  assert.equal(await view.getByRole('button',{name:'Test saved transport'}).count(),0);
  assert.equal((await view.goto(base+'/settings/maps')).status(),403);
  console.log('Persisted mail/maps saves, write-only credentials, coordinate acceptance and delegated-view denial passed.');
 } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exit(1);});
