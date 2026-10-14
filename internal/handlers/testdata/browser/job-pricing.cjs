const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');
(async () => {
  const [base, session, job, item, mode] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({viewport:{width:320,height:900}});
    await context.addCookies([{name:'session',value:session,url:base}]);
    const page = await context.newPage();
    await page.goto(`${base}/jobs/${job}/edit`);
    await page.locator('[data-line-item-editor] input[aria-label="Unit price"]').first().waitFor();
    assert.doesNotMatch(await page.content(),/987\.61|unit_cost/);
    if (mode === 'capture') {
      await page.getByLabel('Catalog item',{exact:true}).first().selectOption(item);
      assert.equal(await page.getByLabel('Line item description',{exact:true}).first().inputValue(),'Captured browser description');
      assert.equal(await page.getByLabel('Captured tax rate',{exact:true}).first().inputValue(),'8.25%');
      await page.getByRole('button',{name:'Add Line',exact:true}).click();
      await page.getByLabel('Line item title',{exact:true}).nth(1).fill('Browser free text');
      await page.getByLabel('Unit price',{exact:true}).nth(1).fill('12.50');
    } else {
      assert.equal(await page.getByLabel('Unit price',{exact:true}).first().inputValue(),'47.25');
      assert.equal(await page.getByLabel('Captured tax rate',{exact:true}).first().inputValue(),'8.25%');
      assert.equal(await page.getByLabel('Catalog item',{exact:true}).first().isDisabled(),true);
      await page.getByLabel('Line item title',{exact:true}).first().fill('Edited browser snapshot');
      await page.getByLabel('Unit price',{exact:true}).first().fill('22.75');
    }
    await page.getByRole('button',{name:'Save',exact:true}).click();
    await page.waitForURL(new RegExp(`/jobs/${job}(?:\\?|$)`));
    assert.match(await page.locator('body').innerText(),/Browser free text/);
    assert.doesNotMatch(await page.content(),/987\.61|unit_cost/);
    await page.reload();
    assert.match(await page.locator('body').innerText(),mode==='capture'?/47\.25/:/22\.75/);
    assert.doesNotMatch(await page.locator('body').innerText(),/8\.25%%/);
    assert.equal(await page.getByRole('region',{name:'Captured Job pricing, scroll for more columns'}).count(),1,'phone pricing must use contained scrolling rather than splitting prices into individual digits');
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth),'pricing must not expand the phone page');
    if (process.env.JOB_PRICING_EVIDENCE) await page.screenshot({path:`${process.env.JOB_PRICING_EVIDENCE}/${mode}-320.png`,fullPage:true});
    console.log(`PASS persisted ${mode}, Cost-free capture, retained provenance and tax, 320px browser`);
  } finally { await browser.close(); }
})().catch(error=>{console.error(error);process.exitCode=1;});
