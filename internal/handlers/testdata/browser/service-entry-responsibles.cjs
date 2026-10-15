const assert=require('node:assert/strict');
const {chromium}=require('playwright-core');
(async()=>{
 const [base,cookie,path,editor,departed]=process.argv.slice(2);
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try {
  const cases=[['Both responsible',[editor,departed]],['Departed solely responsible',[departed]],['Editor solely responsible',[editor]]];
  for(const [summary,expected] of cases){
   const context=await browser.newContext({viewport:{width:390,height:844}});
   await context.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
   const page=await context.newPage();await page.goto(base+path);
   assert.equal(await page.locator(`#service-entry-create option[value="${departed}"]`).count(),0,'former responsible must not become a new-create choice');
   const form=page.locator('form[data-service-revision]').filter({has:page.locator('textarea').filter({hasText:summary})});
   const selected=()=>form.locator('[name=responsible_user_id]').evaluate(el=>[...el.selectedOptions].map(o=>o.value).sort());
   assert.deepEqual(await selected(),[...expected].sort(),`fresh form must retain ${summary}`);
   if(!expected.includes(departed)) assert.equal(await form.locator(`option[value="${departed}"]`).count(),0,'retention choices must be entry-specific');
   if(expected.includes(departed)) assert.match(await form.locator(`option[value="${departed}"]`).innerText(),/Renamed former colleague/);
   await form.locator('[name=summary]').fill(summary+' corrected');
   await form.getByRole('button',{name:'Revise Service Entry',exact:true}).click();
   await page.locator('article').getByText(summary+' corrected',{exact:true}).waitFor();
   await context.close();
   const fresh=await browser.newContext();await fresh.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
   const read=await fresh.newPage();await read.goto(base+path);
   const saved=read.locator('form[data-service-revision]').filter({has:read.locator('textarea').filter({hasText:summary+' corrected'})});
   assert.deepEqual(await saved.locator('[name=responsible_user_id]').evaluate(el=>[...el.selectedOptions].map(o=>o.value).sort()),[...expected].sort(),'persisted summary-only revision changed responsibility');
   await fresh.close();
  }
  console.log('F42-1 fresh non-directory browser: mixed, sole former, and entry-specific choices PASS');
 } finally { await browser.close(); }
})().catch(err=>{console.error(err);process.exitCode=1});
