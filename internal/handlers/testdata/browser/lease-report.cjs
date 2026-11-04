const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
(async () => {
 const [base,session,date] = process.argv.slice(2);
 const browser = await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try {
  const context=await browser.newContext();
  await context.addCookies([{name:'session',value:session,url:base}]);
  const page=await context.newPage();
  for(const width of [1280,320]) {
   await page.setViewportSize({width,height:850});
   await page.goto(base+'/leases/report?as_of='+date+'&search=Historical');
   await page.getByRole('heading',{name:'Lease position report',exact:true}).waitFor();
   assert.equal(await page.getByLabel('As of company date').inputValue(),date);
   assert.equal(await page.getByLabel('Search',{exact:true}).inputValue(),'Historical');
   await page.getByRole('button',{name:'Refresh report'}).click();
   await page.waitForLoadState();
   assert.equal(await page.getByLabel('Search',{exact:true}).inputValue(),'Historical');
   assert.match(await page.locator('body').innerText(),/Updated at/);
   assert.match(await page.locator('body').innerText(),/1 matching Leases/);
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth),true);
  }
 } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exit(1)});
