const assert=require('node:assert/strict');
const {chromium}=require('playwright-core');
(async()=>{
 const [base,cookie,encoded]=process.argv.slice(2),f=JSON.parse(encoded);
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE||'/usr/bin/chromium-browser',args:['--no-sandbox']});
 try {
  const context=await browser.newContext();await context.addCookies([{name:'session',value:cookie,url:base}]);
  const page=await context.newPage();page.on('dialog',dialog=>dialog.accept());
  const posts=[];page.on('request',r=>{if(r.method()==='POST'&&new URL(r.url()).pathname===`/jobs/${f.job}`)posts.push(new URLSearchParams(r.postData()));});
  let faultCustomer=f.a,faultStatus=503;
  let releaseInitial,firstLookup=true;const held=new Promise(resolve=>{releaseInitial=resolve;});
  await page.route('**/contacts/options?*',async route=>{
    if(new URL(route.request().url()).pathname!==`/customers/${faultCustomer}/contacts/options`||!faultStatus)return route.continue();
    if(firstLookup){firstLookup=false;await held;}
    return route.fulfill({status:faultStatus,contentType:'text/html',body:'<h1>Injected lookup failure</h1><option value="0">Untrusted error option</option>'});
  });
  const edit=`${base}/jobs/${f.job}/edit`,select=page.locator('#contact-select');
  await page.goto(edit);
  assert.equal(await select.getAttribute('data-selected'),String(f.contactA),'fixture is an existing persisted linked Job');
  await page.waitForFunction(()=>document.querySelector('#contact-select').dataset.lookupState==='loading');
  assert.equal(await select.inputValue(),String(f.contactA),'pending lookup retains original identity');
  assert.equal(await page.getByRole('button',{name:'Save',exact:true}).isDisabled(),true,'pending is not confirmed empty');
  releaseInitial();
  await page.getByRole('button',{name:'Retry Contact choices',exact:true}).waitFor({timeout:5000});
  assert.equal(await select.inputValue(),String(f.contactA),'503 cannot replace existing relationship with an empty selection');
  assert.doesNotMatch(await select.textContent(),/Injected|Untrusted/,'error HTML is never rendered as options');
  await page.locator('[name=job_type]').fill('Unrelated edit retains linked Contact');
  assert.equal(await page.getByRole('button',{name:'Save',exact:true}).isDisabled(),true,'save blocked while relationship cannot be checked');
  await page.locator('[name=job_type]').press('Enter');
  await page.locator('form[action="/jobs/'+f.job+'"]').evaluate(form=>form.requestSubmit());
  assert.equal(posts.length,0,'neither Enter nor submit dispatches an incomplete relationship');
  faultStatus=0;await page.getByRole('button',{name:'Retry Contact choices',exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('#contact-select').dataset.lookupState==='ready');
  assert.equal(await select.inputValue(),String(f.contactA));
  await page.getByRole('button',{name:'Save',exact:true}).click();await page.locator('.work-item-detail').waitFor();
  assert.equal(posts[0].get('customer_contact_id'),String(f.contactA));
  await page.goto(edit);await page.waitForFunction(()=>document.querySelector('#contact-select').dataset.lookupState==='ready');
  assert.equal(await select.inputValue(),String(f.contactA),'original link survives actual persisted unrelated Save/reload');
  assert.equal(await page.locator('[name=job_type]').inputValue(),'Unrelated edit retains linked Contact');
  for(const status of [403,404]) {
    faultStatus=status;await page.goto(edit);
    await page.waitForFunction(()=>document.querySelector('#contact-select').dataset.lookupState==='denied');
    assert.doesNotMatch(await select.textContent(),/Existing linked contact|Injected|Untrusted/,'unavailable labels are not retained');
    assert.equal(await page.getByRole('button',{name:'Save',exact:true}).isDisabled(),true);
    assert.equal(posts.length,1,'denial is not a request to clear the stored link');
    faultStatus=0;await page.getByRole('button',{name:'Retry Contact choices',exact:true}).click();
    await page.waitForFunction(()=>document.querySelector('#contact-select').dataset.lookupState==='ready');
  }
  faultCustomer=f.b;faultStatus=503;
  await page.locator('#job-customer-select').selectOption(String(f.b));
  for(const [id,oldID] of [['contact-select',f.contactA],['job-project-select',f.projectA],['job-location-select',f.locationA]])
    assert.equal(await page.locator(`#${id} option[value="${oldID}"]`).count(),0,'Customer change immediately removes old-Customer choices');
  await page.getByRole('button',{name:'Retry Contact choices',exact:true}).waitFor();
  assert.equal(await select.inputValue(),'0');assert.equal(await page.getByRole('button',{name:'Save',exact:true}).isDisabled(),true);
  faultStatus=0;await page.getByRole('button',{name:'Retry Contact choices',exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('#contact-select').dataset.lookupState==='ready');
  await select.selectOption(String(f.contactB));
  await page.locator('#job-project-select').selectOption(String(f.projectB));
  await page.locator('#job-location-select').selectOption(String(f.locationB));
  await page.getByRole('button',{name:'Save',exact:true}).click();await page.locator('.work-item-detail').waitFor();
  await page.goto(edit);await page.waitForFunction(()=>document.querySelector('#contact-select').dataset.lookupState==='ready');
  assert.equal(await page.locator('#job-customer-select').inputValue(),String(f.b));
  assert.equal(await select.inputValue(),String(f.contactB));
  assert.equal(await page.locator('#job-project-select').inputValue(),String(f.projectB));
  assert.equal(await page.locator('#job-location-select').inputValue(),String(f.locationB));
  assert.equal(posts.length,2);
  faultStatus=401;await page.goto(edit);
  await page.getByText('Session expired—sign in again. Inaccessible input has been removed.',{exact:true}).waitFor();
  assert.equal(await page.locator('#job-customer-select').count(),0,'expired lookup uses the shared input purge');
  assert.equal(posts.length,2);
  console.log('PASS existing linked Job:503 preserves selection and blocks incomplete Save; Retry + unrelated Save retains persisted Contact;403/404 masks choices and blocks; intentional Customer change removes all old-Customer choices before retry and persists explicit new links');
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
