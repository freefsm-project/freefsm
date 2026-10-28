const assert=require('node:assert/strict');
const {chromium}=require('playwright-core');
(async()=>{
 const [base,creatorCookie,lifecycleCookie,billingCookie,id,customer,location,asset]=process.argv.slice(2);
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try {
  async function pageFor(cookie){const c=await browser.newContext({viewport:{width:390,height:844}});await c.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);const p=await c.newPage();p.on('dialog',d=>d.accept());return p;}
  async function post(page,path,data){const token=await page.locator('input[name=csrf_token]').first().inputValue();return page.context().request.post(base+path,{form:{...data,csrf_token:token},headers:{'Sec-Fetch-Site':'same-origin'},maxRedirects:0});}
  const creator=await pageFor(creatorCookie),life=await pageFor(lifecycleCookie),finance=await pageFor(billingCookie),path=`/leases/${id}`;
  assert.equal((await creator.goto(base+'/leases/new')).status(),200);
  await creator.locator('[name=customer_id]').selectOption(customer);
  await Promise.all([creator.waitForURL(/\/leases\/\d+$/),creator.getByRole('button',{name:'Register Draft',exact:true}).click()]);
  assert.notEqual(new URL(creator.url()).pathname,path);
  await creator.reload();assert.match(await creator.content(),/Required after minimal registration/);
  assert.equal(await creator.getByRole('button',{name:'Archive Draft',exact:true}).count(),0);
  await finance.goto(base+path+'/billing');const oldRevision=await finance.locator('[name=revision]').inputValue();const oldEpoch=await finance.locator('[name=lifecycle_revision]').inputValue();
  await life.goto(base+path);assert.equal(await life.getByRole('button',{name:'Save Draft details',exact:true}).count(),0);
  let wait=life.waitForResponse(r=>r.request().method()==='POST'&&r.url()===base+path+'/delete');await life.getByRole('button',{name:'Archive Draft',exact:true}).click();assert.ok([200,303].includes((await wait).status()));await life.getByText('Archived — read-only history',{exact:true}).waitFor();
  await creator.goto(base+path);assert.equal(await creator.getByRole('button',{name:'Save Draft details',exact:true}).count(),0);assert.equal(await creator.locator('input[type=file]').count(),0);assert.equal(await creator.getByRole('button',{name:'Restore Draft',exact:true}).count(),0);
  await finance.goto(base+path+'/billing');assert.equal(await finance.getByRole('button',{name:'Save Billing terms',exact:true}).count(),0);assert.match(await finance.content(),/5412345/);assert.doesNotMatch(await finance.content(),/Browser base-only history|Preserved required answer/);
  assert.equal((await post(life,`/locations/${location}/delete`,{})).status(),303);
  assert.equal((await post(life,`/assets/${asset}/delete`,{})).status(),303);
  await life.reload();wait=life.waitForResponse(r=>r.request().method()==='POST'&&r.url()===base+path+'/restore');await life.getByRole('button',{name:'Restore Draft',exact:true}).click();assert.ok([200,303].includes((await wait).status()));await life.getByRole('button',{name:'Archive Draft',exact:true}).waitFor();await life.reload();
  assert.match(await life.content(),/Archived Location/);assert.match(await life.content(),/Archived Asset/);assert.equal(await life.locator(`a[href="/locations/${location}"]`).count(),0);assert.equal(await life.locator(`a[href="/assets/${asset}"]`).count(),0);
  await creator.goto(base+path);assert.equal(await creator.locator('[name=title]').inputValue(),'Browser base-only history');assert.match(await creator.content(),/Preserved required answer/);
  // A Billing form from before Archive/Restore cannot silently revive stale intent.
  await finance.goto(base+path+'/billing');assert.equal((await post(finance,path+'/billing',{revision:oldRevision,lifecycle_revision:oldEpoch,asset_id:asset,rent_cents:'1'})).status(),409);
  await finance.goto(base+path+'/billing/history');assert.match(await finance.content(),/5412345/);assert.doesNotMatch(await finance.content(),/Browser base-only history|Preserved required answer/);
  assert.equal((await finance.goto(base+path+'/history')).status(),404);
  await creator.goto(base+path+'/history');assert.match(await creator.content(),/lease.archive|lease.restore/);assert.doesNotMatch(await creator.content(),/5412345|RentCents|TaxRate/);
  console.log('minimal numbered browser registration; independent lifecycle grants; archived basic/Billing readonly; stale retained master badges; state-only restore; facet history; stale Billing ABA denied');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exit(1)});
