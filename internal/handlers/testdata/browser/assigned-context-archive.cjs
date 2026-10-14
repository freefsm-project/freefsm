const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');
(async () => {
  const [base, cookie, job, mode = 'bounded'] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({ viewport: { width: 320, height: 740 } });
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = await context.newPage();
    for (const route of ['master-context', 'customer-context']) {
      assert.equal((await page.goto(`${base}/jobs/${job}/${route}`)).status(), 200);
      const text = await page.locator('body').innerText();
      if (mode === 'detail') {
        assert.match(text, /SENSITIVE-DESCRIPTION/);
        assert.match(text, /SENSITIVE-SERVICE/);
      } else {
        assert.doesNotMatch(text, /SENSITIVE-|37%|100%/);
      }
      if (mode === 'absent') {
        assert.doesNotMatch(text, /Retained Project|Archived/);
      } else {
        assert.match(text, /Retained Project/);
        assert.match(text, /Archived/);
      }
      assert.equal(await page.locator('section[aria-label="Linked Job context"]').count(), 1);
      assert.equal(await page.locator('a[href*="/projects/"]').count(), 0);
    }
    if (process.env.ASSIGNED_CONTEXT_EVIDENCE) await page.screenshot({ path: `${process.env.ASSIGNED_CONTEXT_EVIDENCE}/archive-context-${mode}.png`, fullPage: true });
    console.log(`Archived Project ${mode} authority passed on both browser routes.`);
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
