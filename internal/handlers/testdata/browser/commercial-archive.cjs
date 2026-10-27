const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');
(async () => {
  const [base, cookie, estimate, invoice, phase] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = await context.newPage();
    for (const path of [`/estimates/${estimate}`, `/invoices/${invoice}`]) {
      await page.goto(base + path);
      assert.match(await page.locator('body').innerText(), /Archived on/);
      const restore = page.getByRole('button', { name: 'Restore', exact: true });
      assert.equal(await restore.count(), phase === 'allowed' ? 1 : 0);
      assert.equal(await page.locator(`form[action="${path}/delete"]`).count(), 0);
      if (phase === 'allowed') {
        await Promise.all([page.waitForNavigation(), restore.click()]);
        await page.reload();
        assert.doesNotMatch(await page.locator('body').innerText(), /Archived on/);
      } else {
        const status = await page.evaluate(async path => {
          const csrf = document.querySelector('[name="csrf_token"]').value;
          const response = await fetch(path + '/restore', { method: 'POST', headers: {'Content-Type':'application/x-www-form-urlencoded'}, body: new URLSearchParams({csrf_token:csrf}) });
          return response.status;
        }, path);
        assert.equal(status, phase === 'denied' ? 403 : 409);
        await page.reload();
        assert.match(await page.locator('body').innerText(), /Archived on/);
      }
    }
    console.log(`Persisted commercial Restore ${phase}: authority, controls and endpoint verified`);
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
