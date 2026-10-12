const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');

(async () => {
  const [base, workflowCookie, readerCookie, adminCookie, roleID, profile] = process.argv.slice(2);
  const context = await chromium.launchPersistentContext(profile, {
    executablePath: process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium-browser',
    headless: true, args: ['--no-sandbox'], viewport: { width: 320, height: 640 }, reducedMotion: 'reduce'
  });
  try {
    await context.addCookies([{name:'session',value:workflowCookie,url:base,httpOnly:true}]);
    const page = await context.newPage();
    await page.goto(base+'/preferences');
    for (const path of ['/jobs','/customers','/projects','/assets','/estimates','/invoices','/schedule','/time-entries','/settings','/users']) {
      assert.equal(await page.locator(`#sidebar a[href="${path}"]`).count(),0,'workflow-only hides '+path);
    }
    for (const type of ['job','project','estimate','invoice']) {
      await page.getByRole('button',{name:'Menu',exact:true}).click();
      await page.locator(`#sidebar a[href="/settings/statuses/${type}"]`).click();
      await page.waitForURL(base+'/settings/statuses/'+type);
      assert.equal(await page.locator(`#sidebar a[href="/settings/statuses/${type}"]`).getAttribute('aria-current'),'page');
      await page.locator('.status-settings-back').click();
      await page.waitForURL(base+'/');
    }
    await page.goto(base+'/settings/statuses/job');
    await page.getByRole('button',{name:'Create Custom Status',exact:true}).click();
    const dialog = page.getByRole('dialog',{name:'Create Custom Status',exact:true});
    await dialog.getByLabel('Status label').fill('Navigation persisted workflow');
    await dialog.getByRole('button',{name:'Create status',exact:true}).click();
    await page.waitForURL(base+'/settings/statuses/job');
    await page.locator('.status-chip-name',{hasText:'Navigation persisted workflow'}).waitFor();
    await page.reload();
    assert.equal(await page.locator('.status-chip-name',{hasText:'Navigation persisted workflow'}).count(),1);

    await context.addCookies([{name:'session',value:readerCookie,url:base,httpOnly:true}]);
    await page.setViewportSize({width:1280,height:900});
    await page.goto(base+'/preferences');
    for (const path of ['/jobs','/projects','/assets','/estimates','/invoices','/items','/time-entries','/activity']) {
      await page.locator(`#sidebar a[href="${path}"]`).click();
      await page.waitForURL(base+path);
      assert.equal(await page.locator('#main-content').count(),1,'destination renders '+path);
      assert.equal(await page.locator(`#sidebar a[href="${path}"]`).getAttribute('aria-current'),'page');
    }
    const list = base+'/customers?search=Navigation&sort=name_desc&per_page=25&page=2';
    await page.goto(list);
    const row = page.locator('.work-queue-title').nth(15);
    await row.scrollIntoViewIfNeeded();
    const scroll = await page.evaluate(()=>scrollY);
    await row.click();
    await page.waitForURL(/\/customers\/\d+$/);
    await page.goBack();
    await page.waitForURL(list);
    await page.waitForFunction(y=>Math.abs(scrollY-y)<5,scroll);
    assert.match(await page.locator('.work-queue-title').first().innerText(),/Navigation return 055/);
    await page.goto(base+'/preferences');

    // A second persisted account changes authority through the production form.
    const adminContext = await context.browser().newContext();
    await adminContext.addCookies([{name:'session',value:adminCookie,url:base,httpOnly:true}]);
    const administrator = await adminContext.newPage();
    await administrator.goto(base+'/settings/authorization');
    const form = administrator.locator('form[action="/settings/authorization/roles"]').filter({has:administrator.locator(`input[name=role_id][value="${roleID}"]`)});
    await form.locator('[name=name]').fill('Navigation permission removed');
    await form.locator('summary').click();
    const checked = form.locator('input[name=grant]:checked');
    while(await checked.count()) await checked.first().uncheck();
    await form.getByRole('button',{name:'Save Role and grants',exact:true}).click();
    await administrator.getByRole('heading',{name:'Navigation permission removed',exact:true}).waitFor();
    await page.goBack();
    await page.waitForURL(list);
    await page.waitForFunction(()=>!document.body.innerText.includes('Navigation return'));
    assert.doesNotMatch(await page.locator('body').innerText(),/Navigation return/,'Back never restores revoked records');
    await page.goto(base+'/preferences');
    for (const path of ['/customers','/jobs','/projects','/assets','/estimates','/invoices','/items','/time-entries','/activity','/settings/statuses/job']) {
      assert.equal(await page.locator(`#sidebar a[href="${path}"]`).count(),0,'permission loss removes '+path);
    }
    await adminContext.close();
    console.log('PASS persistent workflow-only mobile configuration, saved workflow, equal-authority destinations, filtered Back/scroll and production grant-loss purge');
  } finally { await context.close(); }
})().catch(error=>{ console.error(error); process.exit(1); });
