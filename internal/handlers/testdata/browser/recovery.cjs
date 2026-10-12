const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');

(async () => {
 const [base, actingCookie, otherCookie, adminCookie, id, mailbox] = process.argv.slice(2);
 const browser = await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium',headless:true,args:['--no-sandbox']});
 try {
  const context = await browser.newContext();
  const page = await context.newPage();
  const pageErrors=[];page.on('pageerror',e=>pageErrors.push(e.message));
  await page.goto(base+'/forgot-password');
  await page.locator('[name=email]').fill('recovery-browser@test');
  await page.getByRole('button',{name:'Send Reset Link'}).click();
  await page.getByText('If an enabled account with that email exists, a reset link has been requested.',{exact:false}).waitFor();
  const token=await (await context.request.get(mailbox)).text();assert.match(token,/^[a-f0-9]{64}$/);
  await page.goto(base+'/reset-password?token='+token);
  assert.equal(await page.locator('[name=password]').getAttribute('minlength'),'8');
  // The genuine production CSRF boundary rejects a forged credential mutation.
  const forged = await context.request.post(base+'/reset-password',{form:{token,password:'ForgedPassword1!',confirm_password:'ForgedPassword1!'}});
  assert.equal(forged.status(),400);
  await page.goto(base+'/reset-password?token='+token);
  await page.locator('[name=password]').fill('BrowserRecovered1!');
  await page.locator('[name=confirm_password]').fill('BrowserRecovered1!');
  await Promise.all([page.waitForNavigation(),page.getByRole('button',{name:'Reset Password',exact:true}).click()]);
  assert.match(page.url(),/\/login/,await page.locator('body').innerText());
  for (const cookie of [actingCookie,otherCookie]) {
   const old = await browser.newContext();await old.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
   const oldPage=await old.newPage();await oldPage.goto(base+'/change-password');assert.match(oldPage.url(),/\/login/);await old.close();
  }
  await page.locator('[name=email]').fill('recovery-browser@test');await page.locator('[name=password]').fill('BrowserRecovered1!');
  await Promise.all([page.waitForNavigation(),page.locator('button[type=submit]').click()]);
  const cookie=(await context.cookies()).find(c=>c.name==='session');assert(cookie.httpOnly);
  await page.goto(base+'/change-password');
  await page.locator('[name=new_password]').fill('Aa1!éé');
  assert.equal(await page.locator('#policy-length').evaluate(el=>el.classList.contains('valid')),false);
  await page.locator('[name=new_password]').fill('Aa1!😀😀');
  assert.equal(await page.locator('#policy-length').evaluate(el=>el.classList.contains('valid')),true);
  await page.locator('[name=current_password]').fill('BrowserRecovered1!');
  const tooLong='Aa1!'+'x'.repeat(69);
  await page.locator('[name=new_password]').fill(tooLong);await page.locator('[name=confirm_password]').fill(tooLong);
  await Promise.all([page.waitForNavigation(),page.getByRole('button',{name:'Change Password',exact:true}).click()]);
  assert.match(await page.locator('body').innerText(),/72 UTF-8 bytes/);
  const exactLimit='Aa1!'+'x'.repeat(68);
  await page.locator('[name=current_password]').fill('BrowserRecovered1!');await page.locator('[name=new_password]').fill(exactLimit);await page.locator('[name=confirm_password]').fill(exactLimit);
  await Promise.all([page.waitForNavigation(),page.getByRole('button',{name:'Change Password',exact:true}).click()]);
  await page.goto(base+'/change-password');assert.match(page.url(),/\/change-password$/);
  const admin=await browser.newContext();await admin.addCookies([{name:'session',value:adminCookie,url:base,httpOnly:true}]);const a=await admin.newPage();
  a.on('pageerror',e=>pageErrors.push(e.message));
  await a.goto(base+'/settings/advanced#tab-security');
  const minimum=a.locator('[name=password_min_length]');
  for (const [value,valid] of [['5',false],['6',true],['72',true],['73',false]]) {
   await minimum.fill(value);assert.equal(await minimum.evaluate(el=>el.checkValidity()),valid);
  }
  await a.goto(base+'/users/'+id);
  await a.locator('summary').filter({hasText:'Reset Password'}).click();
  const form=a.locator('form[action="/users/'+id+'/reset-password"]');
  await form.locator('[name=password]').fill('BrowserTemporary1!');await form.locator('[name=confirm_password]').fill('BrowserTemporary1!');
  await Promise.all([a.waitForNavigation(),form.locator('button[type=submit]').click()]);
  await page.goto(base+'/change-password');assert.match(page.url(),/\/login/);
  await page.locator('[name=email]').fill('recovery-browser@test');await page.locator('[name=password]').fill('BrowserTemporary1!');
  await Promise.all([page.waitForNavigation(),page.locator('button[type=submit]').click()]);assert.match(page.url(),/\/change-password/);
  await page.goto(base+'/customers');assert.match(page.url(),/\/change-password/);
  await page.locator('[name=current_password]').fill('BrowserTemporary1!');await page.locator('[name=new_password]').fill('BrowserFinalPassword1!');await page.locator('[name=confirm_password]').fill('BrowserFinalPassword1!');
  await Promise.all([page.waitForNavigation(),page.getByRole('button',{name:'Change Password',exact:true}).click()]);
  console.log('Public recovery, forged CSRF denial, HTTP-only login, reset revocation, self-change survival, Administrator temporary reset, forced-access denial and final replacement persisted.');
  assert.deepEqual(pageErrors,[]);
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
