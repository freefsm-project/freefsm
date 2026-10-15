const assert=require('node:assert/strict');
const {chromium}=require('playwright-core');
(async()=>{
 const [base,cookie,customer]=process.argv.slice(2);
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try{
  const context=await browser.newContext({viewport:{width:390,height:844}});
  await context.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
  const page=await context.newPage(),errors=[];
  page.on('pageerror',error=>errors.push(error.message));page.on('dialog',dialog=>dialog.accept());
  for(const kind of ['estimate','invoice']){
   const prepare=async(quantity,title)=>{
    await page.goto(`${base}/${kind}s/new?customer_id=${customer}`);
    await page.locator('[name=title]').fill(title);await page.locator('[name=tax_rate]').fill('0');
    if(kind==='invoice'){await page.locator('[name=invoice_date]').fill('2026-10-15');await page.locator('[name=due_date]').fill('');}
    await page.getByLabel('Line item title',{exact:true}).first().fill('Exact tiny quantity');
    const input=page.getByLabel('Quantity',{exact:true}).first();
    assert.equal(await input.getAttribute('type'),'text');assert.equal(await input.getAttribute('inputmode'),'decimal');
    await input.fill(quantity);await page.getByLabel('Unit price',{exact:true}).first().fill('100000.00');
    return input;
   };
   const input=await prepare('0.0000001',`Tiny ${kind}`);
   assert.equal(await input.inputValue(),'0.0000001');assert.equal(await input.evaluate(el=>el.checkValidity()),true);
   await page.waitForFunction(()=>document.querySelector('.line-item-totals').textContent.includes('0.01'));
   let saved=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname===`/${kind}s`);
   await page.getByRole('button',{name:'Save',exact:true}).click();assert.equal((await saved).status(),303);
   await page.waitForURL(new RegExp(`/${kind}s(?:/[0-9]+)?(?:\\?|$)`));
   if(page.url().split('?')[0].endsWith(`/${kind}s`))await page.getByRole('link',{name:`Tiny ${kind}`,exact:true}).first().click();
   await page.waitForURL(new RegExp(`/${kind}s/[0-9]+(?:\\?|$)`));
   assert.match(await page.locator('body').innerText(),/Grand Total: \$0\.01/);
   await page.goto(page.url().split('?')[0]+'/edit');
   assert.equal(await page.getByLabel('Quantity',{exact:true}).first().inputValue(),'0.0000001');
   for(const bad of ['0','-1','NaN']){
    await prepare(bad,`Rejected ${kind} ${bad}`);
    saved=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname===`/${kind}s`);
    await page.getByRole('button',{name:'Save',exact:true}).click();assert.equal((await saved).status(),422);
    await page.waitForLoadState('domcontentloaded');
    assert.equal(await page.getByLabel('Quantity',{exact:true}).first().inputValue(),bad,'invalid input must remain available for correction');
   }
  }
  assert.deepEqual(errors,[]);
  console.log('PASS exact sub-six-decimal quantity preview/save/reopen and nonpositive/nonfinite rejection for both document kinds');
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exit(1)});
