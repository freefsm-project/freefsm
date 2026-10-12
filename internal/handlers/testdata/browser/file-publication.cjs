const { chromium } = require('playwright-core');
(async () => {
  const [base, token, parent] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium', headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext();
    await context.addCookies([{ name: 'session', value: token, url: base }]);
    const page = await context.newPage();
    await page.goto(`${base}/customers/${parent}`);
    let loseResponse = true;
    await page.route('**/files?**', async route => {
      if (!loseResponse) return route.continue();
      loseResponse = false;
      // The production fixture commits File-ID first, then closes the socket.
      await route.continue({headers:{...route.request().headers(),'X-Test-Lose-Response':'true'}});
    });
    await page.locator('input[type=file][name=file]').setInputFiles({name:'browser.txt',mimeType:'text/plain',buffer:Buffer.from('durable browser bytes')});
    await page.locator('.files-drop-form button[type=submit]').click();
    await page.waitForURL(`${base}/customers/${parent}`);
    await page.locator('.files-thumb[data-file-name="browser.txt"]').waitFor();
    const bytes = await page.request.get(base + await page.locator('.files-thumb').getAttribute('data-file-url'));
    if (await bytes.text() !== 'durable browser bytes') throw new Error('download changed exact bytes');
    console.log('Lost publication response reconciled by its original receipt without reupload; persisted exact-byte download verified.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
