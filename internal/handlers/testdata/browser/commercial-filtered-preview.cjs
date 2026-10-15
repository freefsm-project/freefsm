const assert=require('node:assert/strict');
const {chromium}=require('playwright-core');
(async()=>{
 const [base,cookie,customer]=process.argv.slice(2);
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try{
  const context=await browser.newContext({viewport:{width:390,height:844}});
  await context.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
  const page=await context.newPage(),errors=[];page.on('pageerror',error=>errors.push(error.message));
  for(const kind of ['estimate','invoice']){
   await page.goto(`${base}/${kind}s/new?customer_id=${customer}`);
   await page.locator('[name=title]').fill(`Filtered ${kind}`);
   await page.locator('[name=tax_rate]').fill('0');
   if(kind==='invoice'){await page.locator('[name=invoice_date]').fill('2026-10-15');await page.locator('[name=due_date]').fill('');}
   // Keep the initial draft row blank, before the included commercial line.
   await page.getByRole('button',{name:'Add Line',exact:true}).click();
   await page.getByLabel('Line item title',{exact:true}).nth(1).fill('Adjusted populated row');
   await page.getByLabel('Quantity',{exact:true}).nth(1).fill('1');
   await page.getByLabel('Unit price',{exact:true}).nth(1).fill('50.00');
   await page.getByLabel('Discount kind',{exact:true}).nth(1).selectOption('percentage');
   await page.getByLabel('Discount',{exact:true}).nth(1).fill('20');
   await page.getByLabel('Document discount',{exact:true}).fill('4.00');
   await page.getByLabel('Document surcharge kind',{exact:true}).selectOption('percentage');
   await page.getByLabel('Document surcharge',{exact:true}).fill('10');
   const rows=page.locator('[data-line-item-editor] tbody > tr');
   const amount=index=>rows.nth(index).locator('td:nth-last-child(2) span').innerText();
   await page.waitForFunction(()=>document.querySelector('.line-item-totals').textContent.includes('39.60'));
   assert.equal(await amount(0),'—','omitted row must not borrow the populated result');
   assert.equal(await amount(1),'39.60','included row must use staged percentage and document allocations, not the legacy float formula');
   // Inclusion changes must remap both results and then restore the compact map.
   await page.getByLabel('Unit price',{exact:true}).first().fill('10.00');
   await page.getByLabel('Line item title',{exact:true}).first().fill('Temporary included row');
   await page.waitForFunction(()=>document.querySelector('.line-item-totals').textContent.includes('50.60'));
   assert.equal(await amount(0),'10.12');assert.equal(await amount(1),'40.48');
   await page.getByLabel('Line item title',{exact:true}).first().fill('');
   await page.getByRole('button',{name:'Add Line',exact:true}).click();
   await page.waitForFunction(()=>document.querySelector('.line-item-totals').textContent.includes('39.60'));
   assert.equal(await amount(0),'—');assert.equal(await amount(1),'39.60');assert.equal(await amount(2),'—');
   const sent=page.waitForRequest(r=>r.method()==='POST'&&new URL(r.url()).pathname===`/${kind}s`);
   await page.getByRole('button',{name:'Save',exact:true}).click();
   const lines=JSON.parse(new URLSearchParams((await sent).postData()).get('line_items'));
   assert.equal(lines.length,1);assert.equal(lines[0].title,'Adjusted populated row');
   await page.waitForURL(new RegExp(`/${kind}s(?:/[0-9]+)?(?:\\?|$)`));
   if(page.url().split('?')[0].endsWith(`/${kind}s`))await page.getByRole('link',{name:`Filtered ${kind}`,exact:true}).first().click();
   await page.waitForURL(new RegExp(`/${kind}s/[0-9]+(?:\\?|$)`));
   assert.match(await page.locator('body').innerText(),/Grand Total: \$39\.60/);
   await page.goto(page.url().split('?')[0]+'/edit');
   await rows.first().waitFor();
   assert.equal(await rows.count(),1);assert.equal(await amount(0),'39.60');
  }
  assert.deepEqual(errors,[]);
  console.log('PASS filtered commercial preview mapping, inclusion changes, exact allocated row amounts and persisted totals for both document kinds');
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exit(1)});
