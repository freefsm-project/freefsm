const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');

(async () => {
  const [base, adminCookie, token] = process.argv.slice(2);
  const browser = await chromium.launch({executablePath: process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium', headless:true, args:['--no-sandbox']});
  try {
    const admin = await browser.newContext();
    await admin.addCookies([{name:'session',value:adminCookie,url:base,httpOnly:true}]);
    const page = await admin.newPage();
    page.setDefaultNavigationTimeout(15000);
    await page.goto(base+'/users/new');
    await page.locator('[name=name]').fill('Browser manual');
    await page.locator('[name=email]').fill('browser-manual21@example.test');
    await page.locator('[name=role_id]').selectOption('1');
    await page.locator('[name=send_welcome_email]').uncheck();
    await page.locator('[name=password]').fill('BrowserTemporary1!');
    await page.locator('[name=confirm_password]').fill('BrowserTemporary1!');
    await Promise.all([page.waitForNavigation(),page.getByRole('button',{name:'Save',exact:true}).click()]);
    assert.match(page.url(),/\/users/);
    console.log('manual account created');

    const manual = await browser.newContext();
    const person = await manual.newPage();
    person.setDefaultNavigationTimeout(15000);
    await person.goto(base+'/login');
    await person.locator('[name=email]').fill('browser-manual21@example.test');
    await person.locator('[name=password]').fill('BrowserTemporary1!');
    await Promise.all([person.waitForNavigation(),person.locator('form[action="/login"] button[type=submit]').click()]);
    assert.match(person.url(),/\/change-password/);
    console.log('temporary password login restricted');
    for (const path of ['/users/new','/customers','/settings/authorization','/preferences']) {
      await person.goto(base+path);
      assert.match(person.url(),/\/change-password/);
    }
    await person.locator('[name=current_password]').fill('BrowserTemporary1!');
    await person.locator('[name=new_password]').fill('BrowserChosenPassword1!');
    await person.locator('[name=confirm_password]').fill('BrowserChosenPassword1!');
    await Promise.all([person.waitForNavigation(),person.getByRole('button',{name:'Change Password',exact:true}).click()]);
    await person.waitForURL(url=>url.pathname!='/change-password');
    assert.doesNotMatch(person.url(),/\/change-password/,await person.locator('body').innerText());
    assert.equal((await person.goto(base+'/users')).status(),200);
    console.log('temporary password replaced');

    const invited = await browser.newContext();
    const invite = await invited.newPage();
    invite.setDefaultNavigationTimeout(15000);
    await invite.goto(base+'/accept-invite?token='+encodeURIComponent(token));
    await invite.locator('[name=password]').fill('BrowserChosenPassword1!');
    await invite.locator('[name=confirm_password]').fill('BrowserChosenPassword1!');
    await Promise.all([invite.waitForNavigation(),invite.locator('button[type=submit]').click()]);
    await invite.waitForURL(url=>url.pathname==='/login');
    assert.match(invite.url(),/\/login/);
    console.log('invitation redeemed');
    await invite.locator('[name=email]').fill('browser-invited21@example.test');
    await invite.locator('[name=password]').fill('BrowserChosenPassword1!');
    await Promise.all([invite.waitForNavigation(),invite.locator('button[type=submit]').click()]);
    await invite.waitForURL(url=>url.pathname!='/login');
    assert.doesNotMatch(invite.url(),/\/change-password|\/login/);
    assert.equal((await invite.goto(base+'/users')).status(),200);
    await invite.goto(base+'/accept-invite?token='+encodeURIComponent(token));
    assert.match(await invite.locator('body').innerText(),/Invalid or expired invitation/i);
    console.log('SMTP-backed invitation redemption, single-use, manual Administrator temporary password, guessed URL restrictions, replacement, and normal access passed with CSRF.');
  } finally { await browser.close(); }
})().catch(error=>{console.error(error);process.exitCode=1;});
