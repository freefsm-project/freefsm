const {chromium}=require('playwright-core');
const assert=require('node:assert/strict');
(async()=>{
 const base=process.argv[2];
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try {
  const context=await browser.newContext();
  const page=await context.newPage();
  await page.goto(base+'/api/v1/reference');
  async function pair(cookie,label) {
   await context.addCookies([{name:'session',value:cookie,url:base}]);
   await page.getByLabel('Device label').fill(label);
   const started=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/pairings');
   await page.getByRole('button',{name:'Start pairing',exact:true}).click();
   const code=(await (await started).json()).data.user_code;
   const approval=await context.newPage();await approval.goto(base+'/pair');
   await approval.getByLabel('Pairing code').fill(code);
   await approval.getByRole('button',{name:'Review device',exact:true}).click();
   await approval.getByRole('button',{name:'Approve device',exact:true}).click();
   await approval.getByText('Device approved.',{exact:false}).waitFor();
   const redeemed=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/pairings/redeem');
   await page.getByRole('button',{name:'Redeem approved pairing',exact:true}).click();
   const token=(await (await redeemed).json()).data.token;
   await page.getByText('Paired. Bearer held privately in memory.',{exact:true}).waitFor();
   await approval.close();return token;
  }
  const old=await pair(process.env.SECURITY_BROWSER_COOKIE,'Old pending client');
  let deletes=0,operation,receiptMiss=false;
  await page.route('**/api/v1/session',async route=>{
   if(route.request().method()!=='DELETE')return route.continue();
   deletes++;
   if(deletes===1){operation=route.request().headers()['operation-id'];await route.abort('failed');}
   else await route.continue();
  });
  await page.getByRole('button',{name:'Log out this bearer',exact:true}).click();
  await page.locator('#status').filter({hasText:'Response unavailable'}).waitFor();
  // A separate security action invalidates the old client, while the pending
  // UUID was never received. No receipt/admission exists for that UUID.
  const revoked=await context.request.delete(base+'/api/v1/session',{headers:{Authorization:'Bearer '+old,'Operation-ID':require('node:crypto').randomUUID()}});
  assert.equal(revoked.status(),204);
  await page.getByRole('button',{name:'Inspect own sessions',exact:true}).click();
  await page.locator('#status').filter({hasText:'unauthorized'}).waitFor();
  const fresh=await pair(process.env.SECURITY_FRESH_COOKIE,'Fresh authorized client');
  page.on('response',response=>{
   if(new URL(response.url()).pathname==='/api/v1/session-operations/'+operation && response.status()===404)receiptMiss=true;
  });
  await page.getByRole('button',{name:'Log out this bearer',exact:true}).click();
  await page.waitForFunction(()=>!document.getElementById('start').querySelector('button').disabled || !document.getElementById('logout').disabled);
  assert.equal(receiptMiss,true,'real server must report original receipt absent');
  assert.equal(deletes,1,'fresh-auth receipt miss must never replay the old DELETE');
  const stillValid=await context.request.get(base+'/api/v1/me',{headers:{Authorization:'Bearer '+fresh}});
  assert.equal(stillValid.status(),200,'fresh authentication must remain valid');
  assert.match(await page.locator('#status').innerText(),/newly reviewed|new operation|read-only/i);
  console.log('PASS real pairing, lost unreceived logout, revocation, fresh authentication, receipt404: no DELETE and fresh bearer remains valid');
 } finally {await browser.close();}
})().catch(err=>{console.error(err);process.exit(1);});
