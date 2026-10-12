const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');

(async () => {
  const [base, adminCookie, personCookie, userID] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium', headless: true, args: ['--no-sandbox'] });
  try {
    const admin = await browser.newContext();
    await admin.addCookies([{ name: 'session', value: adminCookie, url: base, httpOnly: true }]);
    const page = await admin.newPage();
    assert.equal((await page.goto(base + '/settings/authorization')).status(), 200);
    const create = page.locator('form[action="/settings/authorization/roles"]').filter({ hasNot: page.locator('[name=role_id]') });
    await page.getByText('Create Role', { exact: true }).first().click();
    await create.locator('[name=name]').fill('Browser directory Role');
    await create.locator('summary').click();
    assert.equal(await create.locator('[name=grant]:checked').count(), 0);
    await create.locator('[name=grant][value="directory.view:A"]').check();
    assert.equal(await create.locator('[value$=":S"]').count(), 0);
    await Promise.all([page.waitForNavigation(), create.getByRole('button', { name: 'Create Role', exact: true }).click()]);
    let article = page.locator('article').filter({ has: page.getByRole('heading', { name: 'Browser directory Role', exact: true }) });
    const roleID = await article.locator('form[action="/settings/authorization/roles"] [name=role_id]').inputValue();
    const assignment = page.locator(`form[action="/settings/authorization/assign"]:has([name=user_id][value="${userID}"])`);
    await assignment.locator('[name=role_id]').selectOption(roleID);
    await Promise.all([page.waitForNavigation(), assignment.getByRole('button').click()]);
    article = page.locator('article').filter({ has: page.getByRole('heading', { name: 'Browser directory Role', exact: true }) });
    await article.locator('[name=name]').fill('Browser renamed Role');
    await Promise.all([page.waitForNavigation(), article.getByRole('button', { name: 'Save Role and grants' }).click()]);

    const viewer = await browser.newContext();
    await viewer.addCookies([{ name: 'session', value: personCookie, url: base, httpOnly: true }]);
    const directory = await viewer.newPage();
    assert.equal((await directory.goto(base + '/directory')).status(), 200);
    assert.match(await directory.locator('table').innerText(), /Browser person/);
    assert.doesNotMatch(await directory.locator('table').innerText(), /Role|password|session|recovery/i);
    assert.equal(await directory.locator('a[href^="/users"]').count(), 0);
    assert.equal((await directory.goto(base + '/users/' + userID)).status(), 403);

    await page.goto(base + '/users/' + userID + '/edit');
    await page.locator('[name=name]').fill('Browser renamed person');
    await Promise.all([page.waitForNavigation(), page.getByRole('button', { name: 'Save', exact: true }).click()]);
    assert.match(await page.locator('body').innerText(), /Browser renamed Role/);
    await Promise.all([page.waitForNavigation(), page.getByRole('button', { name: 'Disable', exact: true }).click()]);
    await directory.goto(base + '/directory');
    assert.match(directory.url(), /\/login/);
    await page.goto(base + '/directory');
    const row = page.locator('tr').filter({ hasText: 'Browser renamed person' });
    assert.match(await row.innerText(), /Unavailable/);
    console.log('Persisted Role create/assign/rename, permission-only directory, sealed identity edit, disable/session revocation and unavailable display passed with CSRF.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
