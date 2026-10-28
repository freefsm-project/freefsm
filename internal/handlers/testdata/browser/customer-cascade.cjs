const assert = require('node:assert/strict');
const {chromium} = require('playwright-core');
(async()=>{
 const [base,cookie,adminCookie,id,user,denied]=process.argv.slice(2);
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE||'/usr/bin/chromium-browser',args:['--no-sandbox']});
 try {
  const context=await browser.newContext({viewport:{width:390,height:844}});
  await context.addCookies([{name:'session',value:cookie,url:base}]);
  const admin=await browser.newContext();await admin.addCookies([{name:'session',value:adminCookie,url:base}]);
  const office=await admin.newPage();await office.goto(`${base}/customers/${id}/contacts/new`);
  const page=await context.newPage();const path=`${base}/customers/${id}`;
  await page.goto(path+'/archive');
  assert.match(await page.locator('.page-body').innerText(),/Private review child/);
  assert.match(await page.locator('.page-body').innerText(),/Primary default will be cleared/);
  const oldSnapshot=await page.locator('[name=archive_snapshot]').inputValue();
  await office.getByLabel('First name',{exact:true}).fill('New reviewed child');
  await office.getByRole('button',{name:'Save',exact:true}).click();await office.waitForURL(/\/contacts\/\d+$/);
  const conflict=page.waitForResponse(r=>r.url()===path+'/delete'&&r.request().method()==='POST');
  await page.getByRole('button',{name:'Confirm archive',exact:true}).click();assert.equal((await conflict).status(),409);
  await page.goto(path+'/archive');assert.notEqual(await page.locator('[name=archive_snapshot]').inputValue(),oldSnapshot);
  assert.match(await page.locator('.page-body').innerText(),/New reviewed child/);
  await office.goto(`${base}/settings/authorization`);
  const response=await office.evaluate(async({user,denied})=>{
   const csrf=document.querySelector('[name=csrf_token]').value;
   return (await fetch('/settings/authorization/assign',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:new URLSearchParams({user_id:user,role_id:denied,csrf_token:csrf})})).status;
  },{user,denied});assert.equal(response,200);
  const deniedResponse=page.waitForResponse(r=>r.url()===path+'/delete'&&r.request().method()==='POST');
  await page.getByRole('button',{name:'Confirm archive',exact:true}).click();assert.equal((await deniedResponse).status(),403);
  await page.waitForFunction(()=>!document.querySelector('[name=archive_snapshot]'));
  assert.doesNotMatch(await page.locator('body').innerText(),/Private review child|New reviewed child/);
  console.log('PASS changed preview rejected; current authority denial removes stale review/input');
  await office.goto(path+'/archive');
  let posts=0;const ids=[];
  await office.route('**/customers/'+id+'/delete',async route=>{
   if(route.request().method()==='POST') {posts++;ids.push(new URLSearchParams(route.request().postData()).get('save_action_id'));await route.continue({headers:{...route.request().headers(),'X-Test-Lose-Response':'true'}});}else await route.continue();
  });
  const reconciled=office.waitForRequest(r=>r.method()==='GET' && /\/save-actions\/[^/]+$/.test(new URL(r.url()).pathname));
  await office.getByRole('button',{name:'Confirm archive',exact:true}).click();
  const lookup=await reconciled;
  await office.waitForURL(path);
  assert.equal(posts,1);assert.equal(ids.length,1);assert.ok(ids[0]);
  assert.equal(lookup.url(),`${base}/save-actions/${ids[0]}`);
  const receipt=await admin.request.get(`${base}/save-actions/${ids[0]}`);assert.equal(receipt.status(),200);assert.equal((await receipt.json()).disposition,'saved');
  assert.equal(await office.getByRole('button',{name:'Retry same action',exact:true}).count(),0);
  await office.unroute('**/customers/'+id+'/delete');await office.goto(path);
  assert.match(await office.locator('.page-body').innerText(),/Archived on/);
  console.log('PASS committed response lost; original receipt reconciled, one POST, no new intent; persisted archive');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
