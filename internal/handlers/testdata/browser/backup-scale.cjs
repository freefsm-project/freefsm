// Real Chromium, real HTTP, real native browser file transfer. No route mocks,
// API shortcuts, archive buffers, or template replacement.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright-core');

(async () => {
  const spec = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
  const browser = await chromium.launch({
    executablePath: process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium-browser',
    headless: true,
    downloadsPath: spec.downloadDir,
  });
  const started = Date.now();
  const measurements = { chromium: browser.version(), phases: [], statusCalls: 0 };
  try {
    const errors = [];
    const makePage = async (origin, session) => {
      const context = await browser.newContext({ acceptDownloads: true });
      await context.addCookies([{ name: 'session', value: session, url: origin, httpOnly: true, sameSite: 'Lax' }]);
      const page = await context.newPage();
      page.setDefaultTimeout(20 * 60 * 1000);
      page.on('pageerror', error => errors.push(error.message));
      page.on('response', async response => {
        if (!response.url().includes('/settings/backup/')) return;
        const action = new URL(response.url()).pathname.split('/').pop();
        if (action === 'status') measurements.statusCalls++;
        if (action === 'download') return;
        try {
          const op = await response.json();
          if (op.Phase) measurements.phases.push(`${op.Kind}:${op.Phase}`);
        } catch { /* A failed response is surfaced by the real UI below. */ }
      });
      await page.goto(origin + '/settings/backup');
      return page;
    };
    const source = await makePage(spec.source, spec.sourceSession);
    await source.locator('#backup-create [name=archive_password]').fill('scale-archive-secret');
    await source.locator('#backup-create button').click();
    // Fail promptly on contract errors rather than spending twenty minutes on
    // an invisible form. The rendered production script remains authoritative.
    await source.waitForFunction(() => !document.querySelector('#backup-download').hidden ||
      /Restore complete|failed|unavailable|incorrect/i.test(document.querySelector('#backup-progress').textContent));
    assert.equal(await source.locator('#backup-download').isVisible(), true,
      await source.locator('#backup-progress').textContent());
    measurements.createSeconds = (Date.now() - started) / 1000;
    await source.locator('#backup-download [name=current_password]').fill('account-0');
    const transferStart = Date.now();
    const event = source.waitForEvent('download');
    await source.locator('#backup-download button').click();
    const download = await event;
    assert.equal(await download.failure(), null);
    assert.equal(download.suggestedFilename(), 'freefsm-backup.age');
    const archive = await download.path(); // Native file; never load it into JS.
    const archiveBytes = fs.statSync(archive).size;
    assert.ok(archiveBytes >= spec.minimumBytes);
    measurements.downloadBytes = archiveBytes;
    measurements.downloadSeconds = (Date.now() - transferStart) / 1000;

    const destination = await makePage(spec.destination, spec.destinationSession);
    await destination.locator('#backup-upload [name=archive]').setInputFiles(archive);
    await destination.locator('#backup-upload [name=archive_password]').fill('scale-archive-secret');
    const uploadStart = Date.now();
    // request.postData()/postDataBuffer() are intentionally never called: either
    // would copy the entire archive into the browser automation process.
    const uploaded = destination.waitForResponse(response => response.url().endsWith('/settings/backup/upload'));
    await destination.locator('#backup-upload button').click();
    const uploadResponse = await uploaded;
    assert.equal(uploadResponse.status(), 200);
    const uploadHeaders = await uploadResponse.request().allHeaders();
    assert.equal(uploadHeaders['content-type'], 'application/octet-stream');
    assert.equal(Number(uploadHeaders['content-length']), archiveBytes);
    measurements.uploadSeconds = (Date.now() - uploadStart) / 1000;
    await destination.locator('#backup-review').waitFor({ state: 'visible' });
    measurements.uploadAndValidationSeconds = (Date.now() - uploadStart) / 1000;
    const metadata = await destination.locator('#backup-metadata').textContent();
    assert.match(metadata, /Source: Instance 0/);
    assert.match(metadata, /Captured: \d{4}-/);
    assert.match(metadata, /Release: v1\.2\.3/);
    assert.match(metadata, /Commit: abcdef1234567/);
    assert.match(await destination.locator('#backup-restore').textContent(), /Instance 1/);
    await destination.locator('#backup-restore [name=current_password]').fill('account-1');
    await destination.locator('#backup-restore [name=confirmation]').fill('Instance 1');
    const restoreStart = Date.now();
    await destination.locator('#backup-restore button').click();
    await destination.locator('#backup-progress a[href="/login"]').waitFor();
    measurements.restoreSeconds = (Date.now() - restoreStart) / 1000;
    assert.ok(measurements.statusCalls > 0, 'real capability polling required');
    assert.ok(measurements.phases.includes('upload:ready'));
    assert.ok(measurements.phases.includes('restore:complete'));
    await destination.locator('#backup-progress a[href="/login"]').click();
    await destination.locator('input[name=email]').fill(spec.sourceEmail);
    await destination.locator('input[name=password]').fill('account-0');
    await destination.locator('form[action="/login"] button[type=submit]').click();
    await destination.waitForURL(url => url.pathname !== '/login');
    await destination.goto(spec.destination + '/settings');
    assert.match(await destination.locator('body').textContent(), /Outbound email is disabled\./);
    const email = await destination.evaluate(async () => {
      const token = document.querySelector('input[name=csrf_token]').value;
      const response = await fetch('/settings/test-email', {
        method: 'POST', headers: { 'X-CSRF-Token': token },
      });
      return { status: response.status, text: await response.text() };
    });
    assert.equal(email.status, 503);
    assert.match(email.text, /disabled/i);
    measurements.restoredAccountLogin = true;
    measurements.emailDisabledNotice = true;
    measurements.testEmailStatus = email.status;
    assert.deepEqual(errors, []);
    measurements.totalSeconds = (Date.now() - started) / 1000;
    fs.writeFileSync(path.join(spec.downloadDir, 'browser-measurements.json'), JSON.stringify(measurements));
    console.log(JSON.stringify(measurements));
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });
