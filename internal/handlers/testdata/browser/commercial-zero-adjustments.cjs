const assert=require('node:assert/strict');
const {chromium}=require('playwright-core');
(async()=>{
 const [base,cookie,estimate,invoice]=process.argv.slice(2);
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try{
  const context=await browser.newContext({viewport:{width:390,height:844}});
  await context.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
  const page=await context.newPage(),errors=[];page.on('pageerror',error=>errors.push(error.message));
  for(const [kind,id] of [['estimate',estimate],['invoice',invoice]]){
   const path=`${base}/${kind}s/${id}`;
   await page.goto(path+'/edit');
   assert.equal(await page.getByLabel('Discount',{exact:true}).first().inputValue(),'0.00');
   assert.equal(await page.getByLabel('Surcharge',{exact:true}).first().inputValue(),'0');
   assert.equal(await page.getByLabel('Document discount kind',{exact:true}).inputValue(),'percentage');
   assert.equal(await page.getByLabel('Document discount',{exact:true}).inputValue(),'0');
   assert.match(await page.locator('.line-item-totals').innerText(),/0\.12/);
   await page.locator('[name=notes]').fill('Only metadata changed; retain accepted zeros');
   await Promise.all([page.waitForURL(new RegExp(`/${kind}s/${id}\\?`)),page.getByRole('button',{name:'Save',exact:true}).click()]);
   assert.match(await page.locator('body').innerText(),/Grand Total: \$0\.12/,'metadata-only save must not apply current exemption or discard zero adjustments');
  }
  assert.deepEqual(errors,[]);
  console.log('PASS API-authored zero adjustments and recorded exemption preserved through real browser metadata edits');
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exit(1)});
