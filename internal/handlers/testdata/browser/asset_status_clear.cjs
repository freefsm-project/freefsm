const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');
(async () => {
  const [base, cookie, asset, status] = process.argv.slice(2);
  const browser = await chromium.launch({executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox']});
  try {
    const context = await browser.newContext();
    await context.addCookies([{name: 'session', value: cookie, url: base, httpOnly: true}]);
    const page = await context.newPage();
    const path = base + '/assets/' + asset;
    assert.equal((await page.goto(path + '/edit')).status(), 200);
    assert.equal(await page.locator('[name=asset_status_id]').inputValue(), status);
    await page.locator('[name=asset_status_id]').selectOption({label: 'No Status'});
    await Promise.all([page.waitForNavigation(), page.locator('form.entity-form button[type=submit]').click()]);
    assert.equal(new URL(page.url()).pathname, '/assets/' + asset);
    await page.reload();
    assert.match(await page.locator('main').innerText(), /No Status/);
    assert.doesNotMatch(await page.locator('main').innerText(), /Browser clearable status/);
    await page.goto(path + '/edit');
    assert.equal(await page.locator('[name=asset_status_id]').inputValue(), '0');
    // An active definition can be selected again; retirement then preserves it
    // on an unrelated edit, while still allowing an explicit clear.
    await page.locator('[name=asset_status_id]').selectOption(status);
    await Promise.all([page.waitForNavigation(), page.locator('form.entity-form button[type=submit]').click()]);
    await page.goto(base + '/settings/assets');
    const row = page.locator('tr').filter({has: page.locator('input[value="Browser clearable status"]')});
    await Promise.all([page.waitForNavigation(), row.getByRole('button', {name: 'Retire', exact: true}).click()]);
    await page.goto(path + '/edit');
    assert.equal(await page.locator('[name=asset_status_id]').inputValue(), status);
    await page.locator('[name=name]').fill('Unrelated browser edit');
    await Promise.all([page.waitForNavigation(), page.locator('form.entity-form button[type=submit]').click()]);
    await page.reload();
    assert.match(await page.locator('main').innerText(), /Browser clearable status/);
    await page.goto(path + '/edit');
    await page.locator('[name=asset_status_id]').selectOption({label: 'No Status'});
    await Promise.all([page.waitForNavigation(), page.locator('form.entity-form button[type=submit]').click()]);
    await page.reload();
    assert.match(await page.locator('main').innerText(), /No Status/);
    await page.goto(path + '/edit');
    assert.equal(await page.locator('[name=asset_status_id]').inputValue(), '0');
    assert.equal(await page.locator('[name=asset_status_id] option[value="' + status + '"]').count(), 0);
    await page.goto(base + '/settings/assets');
    await page.locator('tr').filter({has: page.locator('input[value="Browser clearable status"]')}).getByText('Retired', {exact: true}).waitFor();
    console.log('Persisted active/retired No Status clears, active reselection, unrelated retired-status edit, and retired choice exclusion after reload.');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
