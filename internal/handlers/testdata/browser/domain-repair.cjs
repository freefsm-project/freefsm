const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');
(async () => {
  const [base, cookie, project, asset, phase] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium', headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = await context.newPage();
    page.on('dialog', dialog => dialog.accept());
    if (phase === 'hidden') {
      for (const path of [`/projects/${project}`, `/assets/${asset}`]) {
        await page.goto(base + path);
        await Promise.all([page.waitForNavigation(), page.locator('form[action$="/delete"] button').click()]);
        await page.goto(base + path);
        assert.match(await page.locator('body').innerText(), /Archived on/);
        await Promise.all([page.waitForNavigation(), page.getByRole('button', { name: 'Restore', exact: true }).click()]);
        await page.reload();
        assert.doesNotMatch(await page.locator('body').innerText(), /Archived on/);
      }
    }
    const path = `/projects/${project}`;
    await page.goto(base + path + '/edit');
    assert.equal(await page.locator('[name=location_id]').inputValue(), 'keep');
    assert.doesNotMatch(await page.locator('body').innerText(), /SECRET retained site/);
    await page.locator('[name=name]').fill(`Browser unrelated ${phase}`);
    await page.locator('[name=service_notes]').fill(`Service notes ${phase}`);
    if (phase === 'clear') await page.locator('[name=location_id]').selectOption('0');
    await Promise.all([page.waitForURL(/Project\+updated/), page.getByRole('button', { name: 'Save', exact: true }).click()]);
    await page.reload();
    assert.match(await page.locator('body').innerText(), new RegExp(`Service notes ${phase}`));
    await page.goto(base + path + '/edit');
    assert.equal(await page.locator('[name=location_id]').inputValue(), phase === 'clear' ? '0' : 'keep');
    console.log(`Persisted ${phase} Location intent and independent lifecycle passed`);
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
