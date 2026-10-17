const {chromium} = require('playwright-core');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
(async()=>{
  const [base,token,reader]=process.argv.slice(2);
  const profile=fs.mkdtempSync(path.join(process.env.TMPDIR,'time-export-'));
  const context=await chromium.launchPersistentContext(profile,{executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox'],viewport:{width:320,height:740},acceptDownloads:true});
  try {
    await context.addCookies([{name:'session',value:token,url:base}]);
    const page=await context.newPage();
    page.setDefaultTimeout(10000);
    await page.goto(base+'/time-entries');
    await page.getByLabel('Search notes',{exact:true}).fill('Browser exported');
    await page.getByLabel('Group by',{exact:true}).selectOption('job');
    await page.getByRole('button',{name:'Export Timesheet CSV',exact:true}).click();
    await page.waitForURL(u=>u.pathname==='/time-entries/export');
    assert.match(await page.locator('body').innerText(),/Browser exported/);
    assert.match(await page.locator('body').innerText(),/No GPS fields/);
    await page.getByRole('button',{name:'Confirm Timesheet export',exact:true}).click();
    await page.waitForURL(/\/exports\/\d+$/);
    const accepted=page.url();
    await page.goto(base+'/time-entries');
    await page.goto(base+'/exports');
    await page.locator(`a[href="${new URL(accepted).pathname}"]`).click();
    await page.waitForURL(accepted);
    for(let i=0;i<20;i++) {
      const status=await context.request.get(accepted,{headers:{Accept:'application/json'}});
      if((await status.json()).state==='Ready') break;
      await new Promise(r=>setTimeout(r,200));
    }
    await page.reload();
    const downloadPromise=page.waitForEvent('download');
    await page.getByRole('link',{name:/Download CSV/}).click();
    const file=await (await downloadPromise).path();
    const csv=fs.readFileSync(file,'utf8');
    assert.match(csv,/Browser exported fact/);assert.match(csv,/3600000000/);assert.doesNotMatch(csv,/latitude|longitude|captured_at|accuracy/);
    await page.goto(base+'/time-entries');
    await page.getByRole('button',{name:'Export clock GPS CSV',exact:true}).click();
    await page.waitForURL(u=>u.pathname==='/time-entries/export'&&u.searchParams.get('type')==='gps');
    assert.match(await page.locator('body').innerText(),/Missing or explicitly omitted GPS produces no row/);
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'phone export confirmation reflows');
    await context.clearCookies();await context.addCookies([{name:'session',value:reader,url:base}]);
    await page.goto(base+'/time-entries');
    assert.equal(await page.getByRole('button',{name:/Export .* CSV/}).count(),0);
    assert.equal((await page.goto(accepted+'/download')).status(),404);
    console.log('Persisted phone export: filters/group, independent affordances, durable navigation recovery, Ready CSV download, GPS review, requester isolation and read-only denial passed');
  }finally{await context.close();}
})().catch(e=>{console.error(e);process.exit(1)});
