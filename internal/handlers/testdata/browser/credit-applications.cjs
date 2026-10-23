'use strict';
const {chromium}=require('playwright-core');
const assert=require('node:assert/strict');
(async()=>{
 const [base,cookie,invoice,credit,scenario]=process.argv.slice(2);
 const browser=await chromium.launch({headless:true,executablePath:process.env.BROWSER_EXECUTABLE,args:['--no-sandbox']});
 try {
  const context=await browser.newContext();await context.addCookies([{name:'session',value:cookie,url:base}]);
  const page=await context.newPage();page.on('dialog',d=>d.accept());
  const path='/invoices/'+invoice+'/credit';await page.goto(base+path);
  const form=page.locator('form[data-credit-command]');
  assert.equal(await form.count(),1);assert.equal(await form.locator('[name=credit_id]').inputValue(),'');
  assert(!(await page.content()).includes('Application history'));
  await form.locator('[name=credit_id]').selectOption(credit);await form.locator('[name=amount]').fill('60.00');
  await form.locator('[name=effective_date]').fill('2026-01-02');
  const op=await form.locator('[name=idempotency_key]').inputValue();let posts=0;
  await page.route('**/credit-applications',async route=>{
   if(route.request().method()!=='POST')return route.continue();posts++;
   const response=await route.fetch();assert.equal(response.status(),200);await route.abort('failed');
  });
  await form.getByRole('button',{name:'Apply Credit',exact:true}).click();
  await page.getByText('Operation status unknown. Check the original operation before another attempt.',{exact:true}).waitFor();
  assert(!(await page.content()).includes(credit),'source persisted after lost response');
  assert.equal((await page.request.post(base+'/fixture/change-credit-authority')).status(),200);
  let release,seen;const gate=new Promise(r=>release=r),started=new Promise(r=>seen=r);
  await page.route('**'+path,async route=>{seen();await gate;await route.continue();});
  await page.getByRole('button',{name:'Check original operation',exact:true}).click();await started;
  assert(!(await page.content()).includes(credit),'stale source reappeared while refresh pending');release();
  await page.getByText('Operation accepted. Do not repeat it.',{exact:true}).waitFor();
  assert.equal(posts,1);assert.equal(await page.evaluate(()=>sessionStorage.getItem('freefsm.credit-application.pending-operation')),op);
  const receipt=await page.request.get(base+'/credit-application-operations/'+op);assert.equal(receipt.status(),200);
  assert.deepEqual((await receipt.json()).data,{operation_id:op,outcome:'accepted'});
  if(scenario==='revoke-view'){
   assert(!(await page.content()).includes(credit));assert.equal(await page.locator('form[data-credit-command]').count(),0);
  }else{
   assert.equal(await page.locator('[name=credit_id]').count(),0,'reverse action exposes Apply inputs');
   await page.unroute('**'+path);
   await page.getByRole('button',{name:'Start a different operation',exact:true}).click();
   const reverse=page.locator('form[data-credit-command]');await reverse.locator('[name=reason]').fill('Browser correction');
   await reverse.locator('[name=effective_date]').fill('2026-01-02');await reverse.locator('[name=reference]').fill('BROWSER-52');await reverse.locator('[name=notes]').fill('Exact source restored');
   await reverse.getByRole('button',{name:'Reverse Application',exact:true}).click();
   await page.getByText('Operation accepted. Do not repeat it.',{exact:true}).waitFor();
   assert((await page.content()).includes('BROWSER-52'));assert((await page.content()).includes('Exact source restored'));
  }
  console.log(scenario+': independent actions, exact source, one accepted POST, receipt-first current-authority refresh');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exit(1);});
