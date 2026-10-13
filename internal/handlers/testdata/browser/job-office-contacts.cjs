const assert=require('node:assert/strict');
const {chromium}=require('playwright-core');
(async()=>{
 const [base,cookie,path]=process.argv.slice(2);
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE||'/usr/bin/chromium-browser',args:['--no-sandbox']});
 try {
  const context=await browser.newContext({viewport:{width:320,height:900}});
  await context.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
  const page=await context.newPage();
  await page.goto(base+path);
  for(let reload=0;reload<2;reload++){
   await page.getByText('REVIEW_LINKED_ALPHA',{exact:true}).waitFor();
   await page.getByText('REVIEW_LINKED_BETA',{exact:true}).waitFor();
   assert.equal(await page.getByText('REVIEW_UNLINKED_SIBLING',{exact:true}).count(),0);
   assert.equal(await page.getByRole('link',{name:'Manage relationships',exact:true}).count(),0);
   if(reload===0)await page.reload();
  }
  assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
  if(process.env.JOB_READER_SCREENSHOT)await page.screenshot({path:process.env.JOB_READER_SCREENSHOT,fullPage:true});
  console.log('PASS persisted unassigned office read: two explicit Contacts; no Edit or assignment; no sibling disclosure; 320px');
 }finally{await browser.close()}
})().catch(err=>{console.error(err);process.exitCode=1});
