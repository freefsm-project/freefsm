const assert = require('node:assert/strict');
const {chromium} = require('playwright-core');
(async () => {
  const [base, cookie, customer] = process.argv.slice(2);
  const browser = await chromium.launch({executablePath: process.env.BROWSER_EXECUTABLE, headless:true, args:['--no-sandbox']});
  try {
    const context = await browser.newContext();
    await context.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
    const page = await context.newPage();
    for (const [number, ownership, owner] of [['BROWSER-CUSTOMER-034','customer_owned',customer],['BROWSER-COMPANY-034','company_owned','0']]) {
      await page.goto(base+'/assets/new');
      await page.locator('[name=number]').fill(number);
      await page.locator('[name=ownership_class]').selectOption(ownership);
      await page.locator('[name=name]').fill('Browser pump '+ownership);
      await page.locator('[name=asset_type_id]').selectOption({label:'Browser equipment'});
      await page.locator('[name=customer_id]').selectOption(owner);
      if (owner !== '0') {
        await page.locator('[name=location_id] option').filter({hasText:'Browser site'}).waitFor({state:'attached'});
        await page.locator('[name=location_id]').selectOption({label:'Browser site'});
      }
      await page.locator('[name=serial_number]').fill('Repeated serial');
      await page.locator('[name=manufacturer]').fill('Pump maker');
      await page.locator('[name=model]').fill('Model 34');
      await page.locator('[name=installed_at]').fill('2024-01-15');
      await page.locator('[name=warranty_expires]').fill('2027-01-15');
      await page.locator('[name=notes]').fill('Browser service instructions');
      await Promise.all([page.waitForNavigation(),page.locator('form.entity-form button[type=submit]').click()]);
      assert.equal(new URL(page.url()).pathname,'/assets');
      await page.getByRole('link').filter({hasText:number}).click();
      await page.waitForURL(/\/assets\/\d+$/);
      await page.reload();
      let text=await page.locator('main').innerText();
      for(const expected of [number,'Pump maker','Model 34','Browser service instructions']) assert.ok(text.includes(expected),expected+'\n'+text);
      const path=new URL(page.url()).pathname;
      await page.goto(base+path+'/edit');
      assert.equal(await page.locator('[name=customer_id]').isDisabled(),true);
      assert.equal(await page.locator('[name=number]').count(),0);
      assert.equal(await page.locator('[name=ownership_class]').count(),0);
      assert.equal(await page.locator('[name=installed_at]').inputValue(),'2024-01-15');
      assert.equal(await page.locator('[name=warranty_expires]').inputValue(),'2027-01-15');
      await page.locator('[name=name]').fill('Edited '+number);
      await Promise.all([page.waitForNavigation(),page.locator('form.entity-form button[type=submit]').click()]);
      await page.reload();
      text=await page.locator('main').innerText();assert.ok(text.includes('Edited '+number));
      assert.ok(text.includes(ownership==='company_owned'?'Company-owned':'Customer-owned'));
      if(owner==='0')assert.ok(text.includes('Unplaced'));
    }
    // A fresh browser context reads persisted identities and edited details.
    const fresh=await browser.newContext();
    await fresh.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
    const check=await fresh.newPage();await check.goto(base+'/assets');
    const text=await check.locator('main').innerText();
    assert.ok(text.includes('Edited BROWSER-COMPANY-034'));assert.ok(text.includes('Edited BROWSER-CUSTOMER-034'));
    console.log('Both explicit classes, unplaced Company asset, Customer Location, repeated serial, dates, Service Notes and immutable identities persisted across edit and fresh browser context.');
  } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
