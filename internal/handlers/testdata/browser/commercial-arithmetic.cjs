const assert=require('node:assert/strict');
const {chromium}=require('playwright-core');
(async()=>{
 const [base,cookie,customer]=process.argv.slice(2);
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try{
  const context=await browser.newContext({viewport:{width:390,height:844}});
  await context.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
  const page=await context.newPage();
  const errors=[];page.on('pageerror',error=>errors.push(error.message));
  for(const kind of ['estimate','invoice']){
   await page.goto(`${base}/${kind}s/new?customer_id=${customer}`);
   await page.locator('[name=title]').fill(`Exact browser ${kind}`);
   await page.locator('[name=tax_rate]').fill('8.25');
   if(kind==='invoice'){await page.locator('[name=invoice_date]').fill('2026-10-15');await page.locator('[name=due_date]').fill('');}
   const row=async(index,title,quantity,price,taxable)=>{
    await page.getByLabel('Line item title',{exact:true}).nth(index).fill(title);
    await page.getByLabel('Quantity',{exact:true}).nth(index).fill(quantity);
    await page.getByLabel('Unit price',{exact:true}).nth(index).fill(price);
    await page.getByLabel('Taxable line item',{exact:true}).nth(index).setChecked(taxable);
   };
   await row(0,'Fractional work','1.5','10.01',true);
   await page.getByLabel('Discount kind',{exact:true}).first().selectOption('percentage');
   await page.getByLabel('Discount',{exact:true}).first().fill('10');
   await page.getByLabel('Surcharge',{exact:true}).first().fill('0.05');
   await page.getByRole('button',{name:'Add Line',exact:true}).click();await row(1,'Untaxed work','1','5.00',false);
   await page.getByRole('button',{name:'Add Line',exact:true}).click();await row(2,'Signed correction','1','-0.50',true);
   await page.getByLabel('Document discount',{exact:true}).fill('1.00');
   await page.getByLabel('Document surcharge kind',{exact:true}).selectOption('percentage');
   await page.getByLabel('Document surcharge',{exact:true}).fill('10');
   await page.waitForFunction(()=>document.querySelector('.line-item-totals').textContent.includes('19.96'));
   assert.match(await page.locator('.line-item-totals').innerText(),/18\.83[\s\S]*1\.13[\s\S]*19\.96/);
   const response=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname===`/${kind}s`);
   await page.getByRole('button',{name:'Save',exact:true}).click();assert.equal((await response).status(),303);
   await page.waitForURL(new RegExp(`/${kind}s(?:/[0-9]+)?(?:\\?|$)`));
   if(page.url().split('?')[0].endsWith(`/${kind}s`))await page.getByRole('link',{name:`Exact browser ${kind}`,exact:true}).first().click();
   await page.waitForURL(new RegExp(`/${kind}s/[0-9]+(?:\\?|$)`));
   const detail=page.url().split('?')[0];
   assert.match(await page.locator('body').innerText(),/Grand Total: \$19\.96/);
   await page.goto(detail+'/edit');
   assert.equal(await page.getByLabel('Document discount',{exact:true}).inputValue(),'1.00');
   assert.equal(await page.getByLabel('Document surcharge kind',{exact:true}).inputValue(),'percentage');
   assert.equal(await page.getByLabel('Discount kind',{exact:true}).first().inputValue(),'percentage');
   await page.locator('[name=notes]').fill('Metadata only preserves accepted cents');
   await Promise.all([page.waitForURL(new RegExp(`/${kind}s/[0-9]+\\?`)),page.getByRole('button',{name:'Save',exact:true}).click()]);
   assert.match(await page.locator('body').innerText(),/Grand Total: \$19\.96/);
  }
  assert.deepEqual(errors,[]);
  console.log('PASS real phone browser exact preview, persisted amounts, retained adjustments and metadata-only edits for both document kinds');
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exit(1)});
