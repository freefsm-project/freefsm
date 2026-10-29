const assert = require('node:assert/strict');
const {chromium} = require('playwright-core');
(async()=>{
 const [base,viewerCookie,baseCookie,noneCookie,foreignCookie,lease,idsJSON]=process.argv.slice(2);
 const expected=JSON.parse(idsJSON).map(String),old=expected.at(-1),path=`/leases/${lease}/billing`;
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try {
  async function pageFor(value){const context=await browser.newContext();await context.addCookies([{name:'session',value,url:base,httpOnly:true}]);return context.newPage();}
  const page=await pageFor(viewerCookie);
  assert.equal((await page.goto(base+'/leases?facet=billing')).status(),200);
  await page.locator(`a[href="${path}"]`).click();
  await page.getByText('126 billing periods',{exact:true}).waitFor();
  assert.doesNotMatch(await page.content(),/PRIVATE_BASE_COLLAB_NAVIGATION|Coverage changed; review this generated period/);
  assert.equal(await page.getByRole('link',{name:'initial due 2000-01-01',exact:true}).count(),0);
  const nav=()=>page.getByRole('navigation',{name:'Billing period pages',exact:true});
  const rows=()=>page.getByRole('list',{name:'Billing periods',exact:true}).getByRole('listitem');
  await nav().getByRole('link',{name:'Last',exact:true}).click();
  await page.getByText('Page 6 of 6',{exact:true}).waitFor();
  assert.equal(await rows().count(),1);
  await page.getByRole('link',{name:'initial due 2000-01-01',exact:true}).click();
  await page.getByText('Coverage changed; review this generated period',{exact:false}).waitFor();
  let back=page.getByRole('link',{name:'Billing',exact:true});
  assert.equal(new URL(await back.getAttribute('href'),base).searchParams.get('page'),'6');
  await back.click();await page.getByText('Page 6 of 6',{exact:true}).waitFor();
  await nav().getByRole('link',{name:'First',exact:true}).click();
  await page.getByText('Page 1 of 6',{exact:true}).waitFor();
  const actual=[],keys=[];
  for(let number=1;number<=6;number++){
   await page.getByText(`Page ${number} of 6`,{exact:true}).waitFor();
   assert.equal(await rows().count(),number===6?1:25);
   for(const text of await rows().allInnerTexts()){
    const match=text.match(/^(initial|monthly) due (\d{4}-\d{2}-\d{2})/);assert.ok(match,text);keys.push(match[1]+':'+match[2]);
   }
   for(const href of await rows().getByRole('link').evaluateAll(links=>links.map(a=>a.href))){actual.push(new URL(href).pathname.split('/').at(-1));}
   if(number<6)await nav().getByRole('link',{name:'Next',exact:true}).click();
  }
  assert.deepEqual(actual,expected);assert.equal(keys.length,126);assert.equal(new Set(keys).size,126);
  await page.getByRole('navigation',{name:'Billing period filters'}).getByRole('link',{name:'Review required',exact:true}).click();
  await page.getByText('2 billing periods',{exact:true}).waitFor();
  assert.equal(await rows().count(),2);
  await page.getByLabel('Per page',{exact:true}).selectOption('50');
  await page.getByRole('button',{name:'Apply',exact:true}).click();
  await page.waitForURL(url=>url.searchParams.get('filter')==='review'&&url.searchParams.get('per_page')==='50');
  await page.getByRole('link',{name:'initial due 2000-01-01',exact:true}).click();
  await page.getByText('Manual review required',{exact:true}).waitFor();
  assert.match(await page.content(),/Coverage changed; review this generated period/);
  assert.equal(await page.locator('a[href^="/invoices/"]').count(),0);
  assert.equal(await page.getByRole('button',{name:'Replace reviewed Void generation',exact:true}).count(),0);
  const invoice=(await page.locator('body').innerText()).match(/Generation receipt: Invoice (\d+)/)[1];
  assert.equal((await page.context().request.get(base+`/invoices/${invoice}`)).status(),403);
  assert.equal((await page.context().request.get(base+`/leases/${lease}/comments`)).status(),404);
  back=page.getByRole('link',{name:'Billing',exact:true});
  const state=new URL(await back.getAttribute('href'),base).searchParams;
  assert.equal(state.get('filter'),'review');assert.equal(state.get('per_page'),'50');assert.equal(state.get('page'),'1');
  await back.click();await page.getByText('2 billing periods',{exact:true}).waitFor();
  await page.getByRole('navigation',{name:'Billing period filters'}).getByRole('link',{name:'Periods requiring retry',exact:true}).click();
  await page.getByText('3 billing periods',{exact:true}).waitFor();
  assert.equal(await rows().count(),3);
  const unmaterialized=rows().filter({hasText:'monthly due 2000-03-01'});
  assert.match(await unmaterialized.innerText(),/No obligation materialized/);assert.equal(await unmaterialized.getByRole('link').count(),0);
  await page.getByRole('link',{name:'monthly due 2000-02-01',exact:true}).click();
  await page.getByRole('heading',{name:'Lease billing period',exact:true}).waitFor();
  await page.getByText(/Draft generation failed/).waitFor();
  assert.equal(await page.getByRole('button',{name:'Retry reviewed obligation',exact:true}).count(),0);
  await page.getByRole('link',{name:'Billing',exact:true}).click();await page.getByText('3 billing periods',{exact:true}).waitFor();
  assert.equal(new URL(page.url()).searchParams.get('filter'),'failed');
  await page.getByLabel('Filter',{exact:true}).selectOption('all');await page.getByLabel('Per page',{exact:true}).selectOption('100');
  await page.getByRole('button',{name:'Apply',exact:true}).click();await page.getByText('Page 1 of 2',{exact:true}).waitFor();assert.equal(await rows().count(),100);
  await nav().getByRole('link',{name:'Last',exact:true}).click();await page.getByText('Page 2 of 2',{exact:true}).waitFor();assert.equal(await rows().count(),26);
  for(const cookie of [baseCookie,noneCookie,foreignCookie]){
   const denied=await pageFor(cookie);
   for(const url of [path+'?filter=review',path+'?filter=failed',path+`/periods/${old}?filter=review`]){
    assert.equal((await denied.goto(base+url)).status(),404);assert.doesNotMatch(await denied.content(),/billing periods|Coverage changed|5000|2000-01-01/);
   }
  }
  assert.equal((await page.context().request.post(base+'/test/revoke-billing')).status(),204);
  assert.equal((await page.goto(base+path+'?page=6')).status(),404);
  assert.equal((await page.goto(base+path+`/periods/${old}`)).status(),404);
  assert.doesNotMatch(await page.content(),/Coverage changed|5000|2000-01-01/);
  console.log('126 persisted periods; complete bounded navigation without gaps/duplicates; old/new review and failure filters; unmaterialized failure; URL state; Billing-only and current-grant isolation');
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exit(1);});
