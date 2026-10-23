'use strict';
const {chromium}=require('playwright-core');
const assert=require('node:assert/strict');
(async()=>{
 const [base,cookie,customer,scenario]=process.argv.slice(2);
 const browser=await chromium.launch({headless:true,executablePath:process.env.BROWSER_EXECUTABLE,args:['--no-sandbox']});
 try{
  const context=await browser.newContext();await context.addCookies([{name:'session',value:cookie,url:base}]);
  const page=await context.newPage();page.setDefaultTimeout(15000);page.on('dialog',d=>d.accept());
  const path='/customers/'+customer+'/refunds';await page.goto(base+'/customers/'+customer);
  await page.getByRole('link',{name:'Refunds and Payment reversals',exact:true}).click();
  await page.locator('[data-credit-page][data-ready=true]').waitFor();
  const form=page.locator('form[data-credit-command]');assert.equal(await form.count(),1);
  assert(!(await page.content()).includes('Refund history'));
  await form.locator('[name=amount]').fill('70.00');await form.locator('[name=effective_date]').fill('2026-01-04');
  await form.locator('[name=reason]').fill('External refund completed');await form.locator('[name=reference]').fill('REFUND-53');
  const key=await form.locator('[name=idempotency_key]').inputValue();let posts=0,effect;
  await page.route('**/credit/refunds',async route=>{
   if(route.request().method()!=='POST')return route.continue();posts++;
   const response=await route.fetch();assert.equal(response.status(),200);effect=(await response.json()).data.effect_id;
   await route.abort('failed');
  });
  await form.getByRole('button',{name:'Record refund',exact:true}).click();
  await page.getByText('Operation status unknown. Check the original operation before another attempt.',{exact:true}).waitFor();
  assert(!(await page.content()).includes('REFUND-53'));assert.equal(posts,1);
  assert.equal((await page.request.post(base+'/fixture/refund-authority')).status(),200);
  let release,seen;const gate=new Promise(r=>release=r),started=new Promise(r=>seen=r);
  await page.route('**'+path,async route=>{seen();await gate;await route.continue();});
  await page.getByRole('button',{name:'Check original operation',exact:true}).click();await started;
  assert(!(await page.content()).includes('REFUND-53'));release();
  await page.getByText('Operation accepted. Do not repeat it.',{exact:true}).waitFor();
  const receipt=await page.request.get(base+'/refund-operations/'+key);
  assert.deepEqual((await receipt.json()).data,{operation_id:key,outcome:'accepted',effect_id:effect,effect_type:'credit_refund'});
  assert.equal(posts,1);await page.unroute('**'+path);
  if(scenario==='revoke-view'){
   assert.equal(await page.locator('form[data-credit-command]').count(),0);assert(!(await page.content()).includes('REFUND-53'));
  }else{
   const article=page.locator('[data-refund-id]');assert.equal(await article.getAttribute('data-refund-id'),effect);
   const portions=await article.locator('li').allTextContents();assert.equal(portions.length,2);assert(portions[0].includes('40.00'));assert(portions[1].includes('30.00'));
   assert.equal(await page.locator('[name=amount]').count(),0);
   await page.getByRole('button',{name:'Start a different operation',exact:true}).click();
   const reverse=page.locator('[data-refund-id] form');await reverse.locator('[name=reason]').fill('Incorrect refund recording');
   await reverse.getByRole('button',{name:'Reverse Refund',exact:true}).click();
   await page.getByText('Operation accepted. Do not repeat it.',{exact:true}).waitFor();
   assert.deepEqual(await page.locator('[data-refund-id] li').allTextContents(),portions);
   assert((await page.content()).includes('Exact original source portions restored.'));
   await page.getByRole('button',{name:'Start a different operation',exact:true}).click();
   const payment=page.locator('[data-payment-id] form').first();await payment.locator('[name=reason]').selectOption('external_return');
   await payment.locator('[name=reference]').fill('RETURN-53');await payment.locator('[name=notes]').fill('External bank return');
   const reversalKey=await payment.locator('[name=idempotency_key]').inputValue();
   await payment.getByRole('button',{name:'Reverse Payment',exact:true}).click();
   await page.getByText('Operation accepted. Do not repeat it.',{exact:true}).waitFor();assert((await page.content()).includes('RETURN-53'));
   const reversal=await page.request.get(base+'/refund-operations/'+reversalKey);const saved=(await reversal.json()).data;
   assert.equal(saved.effect_type,'payment_reversal');assert.notEqual(saved.effect_id,effect);
   assert.equal((await page.request.post(base+'/fixture/refund-authority?revoke=1')).status(),200);
   await page.getByRole('button',{name:'Check original operation',exact:true}).click();
   await page.getByText('Operation accepted. Do not repeat it.',{exact:true}).waitFor();
   for(const secret of ['RETURN-53','REFUND-53','External bank return',portions[0]])assert(!(await page.content()).includes(secret));
   assert.deepEqual((await (await page.request.get(base+'/refund-operations/'+reversalKey)).json()).data,saved);
   assert.deepEqual((await (await page.request.get(base+'/refund-operations/'+key)).json()).data,{operation_id:key,outcome:'accepted',effect_id:effect,effect_type:'credit_refund'});
  }
  console.log(scenario+': persisted FIFO/refund/reversal; lost response single POST; fresh-authority history and retained exact effect');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exit(1)});
