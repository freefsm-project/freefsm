const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');
(async()=>{
  const [base,cookie,encoded,profile] = process.argv.slice(2);
  const context = await chromium.launchPersistentContext(profile,{executablePath:process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium-browser',headless:true,args:['--no-sandbox'],viewport:{width:1280,height:900}});
  try {
    await context.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
    const page = await context.newPage();
    for(const path of JSON.parse(encoded)) {
      const collection = path.slice(0,path.lastIndexOf('/'));
      const response = await page.goto(base+collection);
      assert.equal(response.status(),200);
      await page.locator(`#main-content a[href="${path}"]`).first().click();
      await page.waitForURL(base+path);
      assert.doesNotMatch(await page.locator('#main-content').innerText(),/DETAIL PRIVATE PRICING|\$123\.45|Record Payment|Create Next Occurrence/);
      await page.locator(`#main-content a[href="${path}/edit"]`).click();
      await page.waitForURL(base+path+'/edit');
      await page.locator(`#main-content form[action="${path}"]`).waitFor();
      assert.doesNotMatch(await page.content(),/DETAIL PRIVATE PRICING|123\.45/,'ungranted pricing absent from editor payload');
      assert.equal(await page.locator(`#main-content form[action="${path}"] button[type=submit]`).count()>0,true);
      await page.reload();
      assert.equal(await page.locator(`#main-content form[action="${path}"]`).count(),1,'persisted edit form reloads');
      if(collection==='/estimates') {
        await page.locator('input[name=title]').fill('Navigation Estimate edited without pricing');
        await page.getByRole('button',{name:'Save',exact:true}).click();
        await page.waitForURL(url=>url.pathname===path);
        assert.match(await page.locator('body').innerText(),/Navigation Estimate edited without pricing/);
        await page.locator(`#main-content a[href="${path}/edit"]`).click();
        await page.waitForURL(base+path+'/edit');
      }
      await page.getByRole('button',{name:'Cancel',exact:true}).click();
      const leave = page.getByRole('dialog',{name:'Unsaved changes'});
      if(await leave.isVisible()) await leave.getByRole('button',{name:'Discard changes',exact:true}).click();
      await page.waitForURL(base+collection);
    }
    console.log('PASS persisted nonprotected tech collection -> detail -> granted edit -> return for Project, Asset, Job, Estimate, Invoice; undisclosed pricing absent from DOM/editors; ordinary Estimate details save succeeds without pricing input');
  } finally { await context.close(); }
})().catch(e=>{console.error(e);process.exit(1)});
