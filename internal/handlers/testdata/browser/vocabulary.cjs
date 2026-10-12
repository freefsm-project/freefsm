const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');
(async () => {
 const [base,cookie,customer] = process.argv.slice(2);
 const browser = await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium',headless:true,args:['--no-sandbox']});
 try {
  const context=await browser.newContext();
  await context.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
  const page=await context.newPage();
  assert.equal((await page.goto(base+'/tags')).status(),200);
  await page.getByRole('button',{name:'New Tag',exact:true}).click();
  await page.locator('[name=name]').fill('\u00a0Browser vocabulary\u2003');
  await Promise.all([page.waitForURL(/\/tags$/),page.getByRole('button',{name:'Save',exact:true}).click()]);
  await page.goto(base+'/customers/'+customer);
  await page.locator('.tag-select').selectOption({label:'Browser vocabulary'});
  await page.locator('.tag-badge').filter({hasText:'Browser vocabulary'}).waitFor();
  await page.goto(base+'/tags');
  await Promise.all([page.waitForNavigation(),page.getByRole('button',{name:'Retire',exact:true}).click()]);
  await page.goto(base+'/tags/new');
  await page.locator('[name=name]').fill('\u202fBROWSER VOCABULARY\u3000');
  const [collision]=await Promise.all([
   page.waitForResponse(response=>response.request().method()==='POST' && new URL(response.url()).pathname==='/tags'),
   page.getByRole('button',{name:'Save',exact:true}).click()
  ]);
  assert.equal(collision.status(),409);
  await page.goto(base+'/customers/'+customer);
  assert.match(await page.locator('#tag-widget').innerText(),/Browser vocabulary.*\(retired\)/s);
  assert.equal(await page.locator('.tag-select option').filter({hasText:'Browser vocabulary'}).count(),0);
  await page.goto(base+'/tags');
  await page.getByRole('button',{name:'Edit',exact:true}).click();
  await page.locator('[name=name]').fill('Browser renamed');
  await page.locator('[name=color]').selectOption('#EF4444');
  await Promise.all([page.waitForURL(/\/tags$/),page.getByRole('button',{name:'Save',exact:true}).click()]);
  await Promise.all([page.waitForNavigation(),page.getByRole('button',{name:'Reactivate',exact:true}).click()]);
  await page.goto(base+'/customers/'+customer);
  assert.match(await page.locator('#tag-widget').innerText(),/Browser renamed/);
  assert.doesNotMatch(await page.locator('#tag-widget').innerText(),/retired/);
  await page.locator('.tag-remove').click();
  await page.locator('.tag-badge').waitFor({state:'detached'});
  await page.locator('.tag-select').selectOption({label:'Browser renamed'});
  await page.locator('.tag-badge').filter({hasText:'Browser renamed'}).waitFor();
  await page.locator('.tag-remove').click();
  await page.locator('.tag-badge').waitFor({state:'detached'});
  await page.goto(base+'/settings/assets');
  const add=page.locator('form[action="/settings/asset-types"]');
  await add.locator('[name=name]').fill('Browser type');
  await Promise.all([page.waitForNavigation(),add.getByRole('button',{name:'Add',exact:true}).click()]);
  const row=page.locator('tr').filter({has:page.locator('input[value="Browser type"]')});
  await Promise.all([page.waitForNavigation(),row.getByRole('button',{name:'Retire',exact:true}).click()]);
  await page.locator('tr').filter({has:page.locator('input[value="Browser type"]')}).getByText('Retired',{exact:true}).waitFor();
  console.log('Persisted CSRF-protected browser: independent tag attach/detach, retire/reactivate, rename/recolor and asset-type retirement.');
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
