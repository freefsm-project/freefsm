const {chromium}=require('playwright-core');
const assert=require('node:assert/strict');
(async()=>{
 const base=process.argv[2];
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try {
  const context=await browser.newContext();
  await context.addCookies([{name:'session',value:process.env.SECURITY_BROWSER_COOKIE,url:base}]);
  const page=await context.newPage();
  await page.goto(base+'/api/v1/reference');
  await page.getByLabel('Device label').fill('Security identity browser');
  const started=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/pairings');
  await page.getByRole('button',{name:'Start pairing',exact:true}).click();
  const code=(await (await started).json()).data.user_code;
  const approval=await context.newPage();await approval.goto(base+'/pair');
  await approval.getByLabel('Pairing code').fill(code);
  await approval.getByRole('button',{name:'Review device',exact:true}).click();
  await approval.getByRole('button',{name:'Approve device',exact:true}).click();
  await approval.getByText('Device approved.',{exact:false}).waitFor();
  await page.getByRole('button',{name:'Redeem approved pairing',exact:true}).click();
  await page.getByText('Paired. Bearer held privately in memory.',{exact:true}).waitFor();
  let deletes=0,operation,dropReceipt=true;
  await page.route('**/api/v1/sessions/'+process.env.SECURITY_TARGET_SESSION,async route=>{
   if(route.request().method()!=='DELETE')return route.continue();
   deletes++;operation=route.request().headers()['operation-id'];
   assert.match(operation,/^[0-9a-f-]{36}$/);
   const committed=await route.fetch();assert.equal(committed.status(),204);
   await route.abort('failed');
  });
  await page.route('**/api/v1/session-operations/*',async route=>{
   assert.equal(new URL(route.request().url()).pathname.split('/').pop(),operation);
   const receipt=await route.fetch();assert.equal(receipt.status(),200);
   if(dropReceipt){dropReceipt=false;await route.abort('failed');}else await route.fulfill({response:receipt});
  });
  await page.getByLabel('Own session ID to revoke').fill(process.env.SECURITY_TARGET_SESSION);
  const click=()=>page.getByRole('button',{name:'Revoke selected session',exact:true}).click();
  await click();await page.locator('#status').filter({hasText:'Response unavailable'}).waitFor();
  await click();await page.waitForFunction(()=>!document.getElementById('revoke').disabled);
  await click();await page.locator('#status').filter({hasText:'No action replayed'}).waitFor();
  assert.equal(deletes,1,'lost DELETE and receipt GET must reconcile without a second mutation');
  await page.getByRole('button',{name:'Inspect own sessions',exact:true}).click();
  await page.locator('#sessions').filter({hasText:'Security target'}).waitFor();
  assert.match(await page.locator('#sessions').innerText(),/Security target.*\(revoked\)/);
  console.log('PASS browser same-ID session DELETE loss + receipt GET loss; one revocation, explicit reconciliation, acting bearer remains authorized');
 } finally {await browser.close();}
})().catch(err=>{console.error(err);process.exit(1);});
