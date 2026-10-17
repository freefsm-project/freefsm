const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

(async () => {
  const [base, raw] = process.argv.slice(2);
  const profile = fs.mkdtempSync(path.join(process.env.TMPDIR, 'time-recovery-'));
  const context = await chromium.launchPersistentContext(profile, {
    executablePath: process.env.BROWSER_EXECUTABLE, headless: true,
    args: ['--no-sandbox'], viewport: { width: 320, height: 740 }, acceptDownloads: true,
  });
  try {
    const page = await context.newPage();
    page.setDefaultTimeout(10000);
    for (const journey of JSON.parse(raw)) {
      await context.clearCookies();
      await context.addCookies([{ name: 'session', value: journey.Token, url: base }]);
      const gps = journey.Kind === 'gps';
      const label = gps ? 'Generate a new clock GPS export' : 'Generate a new Timesheet export';
      const other = gps ? 'Generate a new Timesheet export' : 'Generate a new clock GPS export';
      for (const [origin, code, message] of [
        [journey.Expired, 200, /This result is unavailable/],
        [journey.Expired + '/download', 410, /Export unavailable/],
        [journey.Failed, 200, /Preparation failed/],
      ]) {
        assert.equal((await page.goto(base + origin)).status(), code);
        assert.match(await page.locator('body').innerText(), message);
        assert.doesNotMatch(await page.locator('body').innerText(), /PRIVATE old filter/);
        assert.equal(await page.getByRole('link', { name: other, exact: true }).count(), 0);
        await page.getByRole('link', { name: label, exact: true }).click();
        await page.waitForURL(u => u.pathname === '/time-entries/export');
        assert.equal(new URL(page.url()).searchParams.get('type'), gps ? 'gps' : null);
        assert.doesNotMatch(await page.locator('body').innerText(), /PRIVATE old filter/);
        assert.deepEqual(await page.locator('#export-scope option').evaluateAll(nodes => nodes.map(n => n.value)), ['O']);
        assert.equal(await page.locator('input[name=search]').count(), 0);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'recovery review reflows at 320px');
        await page.getByRole('button', { name: gps ? 'Confirm GPS export' : 'Confirm Timesheet export', exact: true }).click();
        await page.waitForURL(/\/exports\/\d+$/);
        const accepted = page.url();
        assert.notEqual(new URL(accepted).pathname, journey.Expired);
        assert.notEqual(new URL(accepted).pathname, journey.Failed);
        // Recover the committed new request through ordinary navigation.
        await page.goto(base + '/exports');
        await page.locator(`a[href="${new URL(accepted).pathname}"]`).click();
        await page.waitForURL(accepted);
        let ready = false;
        for (let i = 0; i < 30; i++) {
          const response = await context.request.get(accepted, { headers: { Accept: 'application/json' } });
          if ((await response.json()).state === 'Ready') { ready = true; break; }
          await new Promise(resolve => setTimeout(resolve, 200));
        }
        assert.ok(ready, 'new reviewed export becomes Ready');
        await page.reload();
        const downloading = page.waitForEvent('download');
        await page.getByRole('link', { name: 'Download CSV', exact: true }).click();
        const csv = fs.readFileSync(await (await downloading).path(), 'utf8');
        if (gps) {
          assert.match(csv, /latitude,longitude,captured_at,accuracy/);
          assert.match(csv, /12.5,23.5/);
          assert.doesNotMatch(csv, /duration_microseconds|revisions/);
        } else {
          assert.match(csv, /duration_microseconds/);
          assert.doesNotMatch(csv, /latitude|longitude|captured_at|accuracy/);
        }
        console.log(`${journey.Kind}: ${origin.endsWith('/download') ? 'unavailable download' : origin === journey.Failed ? 'Failed' : 'expired'} → current Own confirmation → new durable Ready CSV downloaded`);
      }
    }
  } finally { await context.close(); }
})().catch(error => { console.error(error); process.exit(1); });
