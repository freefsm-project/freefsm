const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');
(async()=>{
  const [base,cookie,id,profile] = process.argv.slice(2);
  const context = await chromium.launchPersistentContext(profile,{executablePath:process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium-browser',headless:true,args:['--no-sandbox'],viewport:{width:1280,height:900}});
  try {
    await context.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
    const page=await context.newPage();
    const job='/jobs/'+id;
    await page.goto(base+job);
    const prepareForm=page.locator(`form[action="${job}/create-invoice"]`);
    const prepareID=await prepareForm.locator('[name=save_action_id]').inputValue();
    await prepareForm.getByRole('button',{name:'Create Invoice',exact:true}).click();
    await page.waitForURL(base+job+'/create-invoice');
    const invoiceForm=page.locator(`form[action="${job}/invoices"]`);
    await invoiceForm.waitFor();
    const prepared=await (await context.request.get(base+'/save-actions/'+prepareID)).json();
    assert.equal(prepared.disposition,'completed','editor preparation is not a saved Invoice');
    const createID=await invoiceForm.locator('[name=save_action_id]').inputValue();
    await invoiceForm.locator('[name=title]').fill('Browser saved Job Invoice');
    await invoiceForm.getByRole('button',{name:'Save',exact:true}).click();
    await page.waitForURL(/\/invoices\/\d+(?:\?.*)?$/);
    await page.reload();
    assert.match(await page.locator('.page-title').innerText(),/Browser saved Job Invoice/);
    assert.match(await page.locator('#main-content').innerText(),/Browser captured line/);
    assert.match(await page.locator('#main-content').innerText(),/\$77\.00/);
    const created=await (await context.request.get(base+'/save-actions/'+createID)).json();
    assert.equal(created.disposition,'saved','actual creation has durable domain confirmation');

    await page.goto(base+job);
    const estimateForm=page.locator(`form[action="${job}/create-estimate"]`);
    const estimateID=await estimateForm.locator('[name=save_action_id]').inputValue();
    page.once('dialog',dialog=>dialog.accept());
    await estimateForm.getByRole('button',{name:'Create Estimate',exact:true}).click();
    await page.waitForURL(/\/estimates\/\d+(?:\?.*)?$/);
    await page.reload();
    assert.match(await page.locator('.page-title').innerText(),/Browser document source/);
    assert.match(await page.locator('#main-content').innerText(),/Browser captured line/);
    const estimate=await (await context.request.get(base+'/save-actions/'+estimateID)).json();
    assert.equal(estimate.disposition,'saved','Create Estimate persists its actual document');
    console.log('PASS unassigned nonprotected tech Job detail -> Invoice preparation(completed) -> Save(saved), and Create Estimate(saved), with persisted copied lines and independent action identities');
  } finally { await context.close(); }
})().catch(e=>{console.error(e);process.exit(1)});
