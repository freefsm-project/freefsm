const assert = require('node:assert/strict');
const {chromium} = require('playwright-core');
(async () => {
  const [base,cookie,id] = process.argv.slice(2);
  const browser = await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
  try {
    const context = await browser.newContext({viewport:{width:320,height:844},acceptDownloads:true});
    await context.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
    const page = await context.newPage();
    await page.goto(base+`/assets/${id}/edit`);
    await page.locator('[name=asset_type_id]').selectOption({label:'Revised browser Type'});
    await page.locator('[name=asset_status_id]').selectOption({label:'Revised browser Status'});
    await page.locator('[name=warranty_expires]').fill('2028-02-16');
    await Promise.all([page.waitForNavigation(),page.locator('form.entity-form button[type=submit]').click()]);
    await page.goto(base+`/assets/${id}/edit`);
    assert.equal(await page.locator('[name=warranty_expires]').inputValue(),'2028-02-16');
    await page.goto(base+`/activity?object_type=asset&object_id=${id}&action=updated`);
    const facts = await page.locator('.activity-facts').innerText();
    for(const value of ['asset_type_id','asset_status_id','warranty_expires','Original browser Type','Revised browser Type','Original browser Status','Revised browser Status']) assert.ok(facts.includes(value),value);
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth));
    await page.getByRole('link',{name:'Export activity CSV',exact:true}).click();
    await Promise.all([page.waitForURL(/\/exports\/\d+$/),page.getByRole('button',{name:'Confirm activity export',exact:true}).click()]);
    for(let n=0;n<30&&!(await page.getByRole('link',{name:'Download CSV',exact:true}).count());n++){await page.waitForTimeout(200);await page.reload();}
    const [download]=await Promise.all([page.waitForEvent('download'),page.getByRole('link',{name:'Download CSV',exact:true}).click()]);
    assert.equal(await download.failure(),null);let csv='';for await(const chunk of await download.createReadStream()) csv+=chunk;
    for(const value of ['asset_type_id','asset_status_id','warranty_expires','Revised browser Type']) assert.ok(csv.includes(value),value);
    console.log('PASS persisted 320px Asset Type/Status/warranty edits, structured history/current vocabulary, exact CSV and reflow.');
  } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exit(1);});
