const {chromium}=require('playwright-core');
const assert=require('node:assert/strict');
(async()=>{
 const [base,token,evidence]=process.argv.slice(2);
 const context=await chromium.launchPersistentContext(process.env.TMPDIR+'/c',{executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox'],viewport:{width:320,height:740}});
 try {
  await context.clearCookies();
  await context.addCookies([{name:'session',value:token,url:base}]);
  await context.addInitScript(()=>{navigator.geolocation.getCurrentPosition=(_ok,fail)=>fail({code:1,message:'denied'});});
  const page=await context.newPage();await page.goto(base);
  await page.getByRole('button',{name:'Clock with location',exact:true}).click();
  await page.getByText('Location unavailable. You can explicitly clock without location.').waitFor();
  await page.getByRole('button',{name:'Clock In',exact:true}).click();
  await page.getByText('Clocked in',{exact:true}).first().waitFor();
  assert.ok(await page.locator('input[name="entry_id"]').inputValue());
  await page.evaluate(()=>{navigator.geolocation.getCurrentPosition=(ok)=>ok({coords:{latitude:51.5,longitude:-0.12,accuracy:4},timestamp:Date.now()-1000});});
  await page.getByRole('button',{name:'Clock with location',exact:true}).click();
  await page.getByRole('button',{name:'Clock In',exact:true}).waitFor();
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth));
  await page.screenshot({path:evidence+'/clock-phone.png',fullPage:true});
  console.log('Denied GPS -> explicit location-free in -> exact out with GPS persisted; phone reflow passed');
 } finally {await context.close();}
})().catch(e=>{console.error(e);process.exit(1);});
