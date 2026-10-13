const {chromium}=require('playwright-core');
const assert=require('node:assert/strict');
(async()=>{
 const base=process.argv[2];
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE||'/usr/bin/chromium',headless:true,args:['--no-sandbox']});
 try {
  const context=await browser.newContext();
  await context.addCookies([{name:'session',value:process.env.PAIRING_BROWSER_COOKIE,url:base}]);
  const page=await context.newPage();let secret='',deviceSecret='',redeems=0;const leaks=[];
  page.on('console',msg=>{if(secret&&msg.text().includes(secret))leaks.push('console');});
  page.on('request',request=>{
   const path=new URL(request.url()).pathname;
   if(path==='/api/v1/pairings/redeem')redeems++;
   if(path.startsWith('/api/v1/')&&!['/api/v1/reference','/api/v1/reference.js'].includes(path)) {
    if(request.headers().cookie)leaks.push('API cookie');
    if((request.postData()||'').includes('password'))leaks.push('password');
   }
   if(secret&&request.url().includes(secret))leaks.push('URL bearer');
   if(deviceSecret&&request.url().includes(deviceSecret))leaks.push('URL device secret');
  });
  await page.goto(base+'/api/v1/reference');
  await page.getByLabel('Device label').fill('Browser protocol tablet');
  const startResponse=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/pairings'&&r.request().method()==='POST');
  await page.getByRole('button',{name:'Start pairing',exact:true}).click();
  const started=await (await startResponse).json();deviceSecret=started.data.device_code;
  const code=started.data.user_code;
  // Browser CSRF blocks a forged approval even with an authenticated cookie.
  const csrf=await context.request.post(base+'/pair/approve',{form:{review_token:'forged',device_name:'Browser protocol tablet'}});
  assert.ok([400,403].includes(csrf.status()),'forged browser approval must fail CSRF');
  const approval=await context.newPage();await approval.goto(base+'/pair');
  assert.equal(await approval.locator('[name=save_action_id]').count(),0,'pairing must use its own protocol');
  await approval.getByLabel('Pairing code').fill(code);
  const reviewResponse=approval.waitForResponse(r=>new URL(r.url()).pathname==='/pair/review'&&r.request().method()==='POST');
  await approval.getByRole('button',{name:'Review device',exact:true}).click();
  assert.equal((await reviewResponse).status(),200,'browser review status');
  await approval.getByRole('heading',{name:'Browser protocol tablet',exact:true}).waitFor();
  await approval.getByRole('button',{name:'Approve device',exact:true}).click();
  await approval.getByText('Device approved.',{exact:false}).waitFor();
  const redeemedResponse=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/pairings/redeem');
  await page.getByRole('button',{name:'Redeem approved pairing',exact:true}).click();
  const redeemed=await (await redeemedResponse).json();secret=redeemed.data.token;
  assert.equal(typeof secret,'string');assert.equal(secret.length,64);
  await page.getByRole('button',{name:'Inspect own sessions',exact:true}).click();
  await page.locator('#sessions').filter({hasText:'Browser protocol tablet'}).waitFor();
  assert.ok(!(await page.content()).includes(secret),'bearer in DOM');
  assert.ok(!(await page.content()).includes(deviceSecret),'device secret in DOM');
  assert.deepEqual(await page.evaluate(()=>({local:localStorage.length,session:sessionStorage.length})),{local:0,session:0});
  assert.equal(await page.evaluate(async()=> (await navigator.serviceWorker.getRegistrations()).length),0);
  if(process.env.PAIRING_SCREENSHOT)await page.screenshot({path:process.env.PAIRING_SCREENSHOT,fullPage:true});
  await approval.goto(base+'/sessions');
  const card=approval.locator('article').filter({has:approval.getByRole('heading',{name:'mobile Browser protocol tablet',exact:true})});
  await card.getByRole('button',{name:'Revoke session',exact:true}).click();
  await card.getByText('Revoked:',{exact:false}).waitFor();
  await page.getByRole('button',{name:'Inspect own sessions',exact:true}).click();
  await page.locator('#status').filter({hasText:'unauthorized'}).waitFor();
  assert.equal(await page.getByRole('button',{name:'Inspect own sessions',exact:true}).isDisabled(),true);
  assert.equal(redeems,1,'automatic replay');assert.deepEqual(leaks,[]);
  await page.reload();assert.equal(await page.getByRole('button',{name:'Inspect own sessions',exact:true}).isDisabled(),true);
  console.log('PASS persisted pairing, browser CSRF, labelled issuance, own revocation, secret-safe interactive harness, no replay/storage/offline mode');
 } finally {await browser.close();}
})().catch(error=>{console.error(String(error.stack||error).replace(/[a-f0-9]{64}/gi,'[redacted]').replaceAll(process.env.PAIRING_BROWSER_COOKIE||'no-cookie','[redacted]'));process.exit(1);});
