const assert = require('node:assert/strict');
const {chromium} = require('playwright-core');
(async () => {
  const [base,cookie,id,state,attempts,next,reason,action] = process.argv.slice(2);
  const browser = await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
  try {
    const context = await browser.newContext({viewport:{width:390,height:844}});
    await context.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
    const page = await context.newPage();
    await page.goto(`${base}/estimates/${id}`);
    await page.reload();
    const item = page.locator('.delivery-history-item').first();
    assert.equal(await item.locator('header strong').innerText(),state);
    assert.match(await item.innerText(),new RegExp(`Attempts: ${attempts}`));
    assert.equal((await item.innerText()).includes('Next attempt:'),next==='yes');
    assert.match(await item.innerText(),/Open tracking disabled/);
    await item.getByText('Attempt history',{exact:true}).click();
    if(reason) assert.ok((await item.innerText()).includes(reason),await item.innerText());
    assert.ok((await item.locator('ol li').count())>0);
    if(action==='retry-lost-response') {
      const form=item.locator('form');
      await form.locator('[name=reason]').fill('Reviewed frozen browser retry');
      let captured;
      await page.route('**/deliveries/*/retry',async route=>{
        captured={url:route.request().url(),body:route.request().postData(),headers:await route.request().allHeaders()};
        const response=await route.fetch();assert.equal(response.status(),200);
        await route.abort('failed');
      });
      await form.getByRole('button',{name:'Queue retry'}).click();
      await page.waitForTimeout(300);
      assert.ok(captured);
      await page.unroute('**/deliveries/*/retry');
      delete captured.headers['content-length'];delete captured.headers.host;
      const replay=await context.request.post(captured.url,{data:captured.body,headers:captured.headers});
      assert.equal(replay.status(),409,await replay.text());
      const receipt=await replay.json();assert.equal(receipt.disposition,'saved');assert.equal(receipt.committed_steps,1);
      assert.equal(receipt.effects[0].action,'estimate.delivery.retry');
      await page.goto(`${base}/estimates/${id}`);await page.reload();
      assert.equal(await page.locator('.delivery-history-item').count(),1);
      assert.equal(await page.locator('.delivery-history-item header strong').innerText(),'queued');
      assert.match(await page.locator('.delivery-history-item').innerText(),new RegExp(`Attempts: ${attempts}`));
    }
    console.log('PASS persisted Chromium Delivery state/attempts/next/reason',state,action||'');
  } finally { await browser.close(); }
})().catch(err=>{console.error(err);process.exit(1)});
