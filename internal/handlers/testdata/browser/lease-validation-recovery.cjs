const assert=require('node:assert/strict');
const {chromium}=require('playwright-core');
(async()=>{
 const [base,cookie,id,oldFile,newFile,field,lost]=process.argv.slice(2);
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try {
  const context=await browser.newContext({viewport:{width:390,height:844}});
  await context.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
  const page=await context.newPage();page.on('dialog',d=>d.accept());const path=`/leases/${id}`,url=base+path;
  const form=()=>page.locator(`form[action="${path}"]`);
  const receipt=async op=>{const r=await context.request.get(base+'/save-actions/'+op);assert.equal(r.status(),200);return r.json();};
  const selectedFromHTML=body=>page.evaluate(html=>{const doc=new DOMParser().parseFromString(html,'text/html');return doc.querySelector('[name=agreement_file_id]')?.value;},body);
  async function submit(){const pending=page.waitForResponse(r=>r.request().method()==='POST'&&r.url()===url);await form().getByRole('button',{name:'Save Draft details',exact:true}).click();return pending;}
  async function correction(selection,old,revision,label){
   assert.equal((await page.goto(url)).status(),200);
   assert.equal(await form().locator('[name=agreement_file_id]').inputValue(),old);
   await form().locator('[name=agreement_file_id]').selectOption(selection);
   await form().locator(`[name=custom_field_${field}]`).fill('');
   await form().locator('[name=title]').fill(label);
   const failure=await submit();assert.equal(failure.status(),422);
   const op=failure.headers()['x-freefsm-action'];assert.ok(op);
   assert.equal(failure.headers()['x-freefsm-disposition'],'rejected');
   assert.equal(await selectedFromHTML(await failure.text()),selection,'server must preserve intent independently of client rehydration');
   await page.waitForFunction(([p,op])=>{const f=document.querySelector(`form[action="${p}"]`);return f?.querySelector('[name=save_action_id]')?.value!==op&&document.querySelector('[data-error-summary]');},[path,op]);
   assert.equal(await form().locator('[name=agreement_file_id]').inputValue(),selection);
   const rejected=await receipt(op);assert.equal(rejected.disposition,'rejected');assert.equal(rejected.retry_safe,true);assert.equal(rejected.committed_steps,0);
   const unchanged=await context.request.get(url);assert.equal(await selectedFromHTML(await unchanged.text()),old);
   await form().locator(`[name=custom_field_${field}]`).fill('Reviewed '+label);
   const success=await submit();assert.equal(success.status(),303);
   const corrected=success.headers()['x-freefsm-action'];assert.ok(corrected&&corrected!==op,'correction must get a new identity');
   await page.waitForFunction(([p,n])=>document.querySelector(`form[action="${p}"] [name=revision]`)?.value===n,[path,String(revision+1)]);
   await page.waitForLoadState('load');await page.reload();
   assert.equal(await form().locator('[name=agreement_file_id]').inputValue(),selection);
   assert.equal((await receipt(corrected)).disposition,'saved');assert.equal((await receipt(op)).disposition,'rejected');
  }
  await correction(newFile,oldFile,1,'replacement intent');
  await correction('',newFile,2,'explicit clear intent');

  // Drop the actual HTTP response after dispatch while durable completion is
  // independently unavailable. Committed audit steps are not a whole receipt.
  await page.goto(url);await form().locator('[name=agreement_file_id]').selectOption(newFile);
  await form().locator('[name=save_action_id]').evaluate((node,id)=>node.value=id,lost);
  let sentBody='',dispatches=0;
  await page.route(url,async route=>{
   if(route.request().method()!=='POST'){await route.continue();return;}
   dispatches++;sentBody=route.request().postData();await route.fetch();await route.abort('failed');
  });
  await form().getByRole('button',{name:'Save Draft details',exact:true}).click();
  await page.waitForFunction(p=>document.querySelector(`form[action="${p}"] [data-action-state="unknown"]`),path);
  const unknown=await receipt(lost);assert.equal(unknown.disposition,'unknown');assert.equal(unknown.retry_safe,false);assert.ok(unknown.committed_steps>0);
  assert.equal(await form().locator('[name=save_action_id]').inputValue(),lost);
  assert.equal(await form().locator('button[type=submit]').evaluate(button=>button.inert),true);
  const current=await context.request.get(url);assert.equal(await selectedFromHTML(await current.text()),newFile);
  const replay=await context.request.post(url,{data:sentBody,headers:{'Content-Type':'application/x-www-form-urlencoded','X-FreeFSM-Action':lost,'Sec-Fetch-Site':'same-origin'},maxRedirects:0});
  assert.equal(replay.status(),409);assert.equal((await replay.json()).disposition,'unknown');assert.equal(dispatches,1);
  console.log('replacement and explicit clear preserved through server 422, rejected receipts, new-identity correction and reload; dropped response with missing completion remains unknown/inert without replay');
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exit(1)});
