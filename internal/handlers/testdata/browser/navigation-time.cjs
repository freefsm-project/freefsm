const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');
(async()=>{
  const [base,cookie,id,profile] = process.argv.slice(2);
  const context = await chromium.launchPersistentContext(profile,{executablePath:process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium-browser',headless:true,args:['--no-sandbox'],viewport:{width:1280,height:900}});
  try {
    await context.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
    const page=await context.newPage();
    const path='/time-entries/'+id;
    await page.goto(base+'/time-entries');
    await page.locator(`.work-queue-table a[href="${path}"]`).click();
    await page.waitForURL(base+path);
    await page.locator('#main-content').waitFor();
    assert.doesNotMatch(await page.locator('#main-content').innerText(),/Private time browser Job|51\.234567/);
    await page.getByRole('button',{name:'Edit',exact:true}).click();
    await page.waitForURL(base+path+'/edit');
    assert.equal(await page.locator('[name=job_id]').count(),0,'inaccessible Job association is not submitted');
    await page.locator('textarea[name=notes]').fill('Persisted time correction').catch(async error=>{console.error('Time editor failure',page.url(),await page.locator('body').innerText());throw error;});
    await page.getByRole('button',{name:'Save',exact:true}).click();
    await page.waitForURL(url=>url.pathname==='/time-entries');
    await page.locator(`.work-queue-table a[href="${path}"]`).click();
    await page.waitForURL(base+path);
    await page.reload();
    assert.match(await page.locator('#main-content').innerText(),/Persisted time correction/);
    assert.doesNotMatch(await page.locator('#main-content').innerText(),/Private time browser Job|51\.234567/);
    await page.getByRole('link',{name:'Back to Timesheets',exact:false}).click();
    await page.waitForURL(base+'/time-entries');
    console.log('PASS nonprotected tech Timesheet row -> other subject detail -> current-grant edit -> persisted correction -> return; hidden Job/GPS never disclosed or cleared');
  } finally { await context.close(); }
})().catch(e=>{console.error(e);process.exit(1)});
