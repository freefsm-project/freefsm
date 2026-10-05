const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium-browser', headless: true });
  try {
    for (const initial of [
      { backup: 'encrypting', upload: 'queued', restore: 'queued' },
      { backup: 'complete', upload: 'validating', restore: 'complete' },
      { backup: 'queued', upload: 'ready', restore: 'queued' },
    ]) {
    const page = await browser.newPage({ acceptDownloads: true });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    let statusCalls = 0;
    const op = { ID: 'secret-capability', Kind: 'backup', Phase: 'complete', SourceName: 'Source <script>unsafe</script>', BuildKind: 'development', Version: 'dev', Commit: 'none', CapturedAt: '2026-10-05T12:00:00Z', ArchiveDigest: 'review-digest' };
    let pending = [];
    let last = op;
    let failNextStatus = '';
    await page.route('**/*', async route => {
      const request = route.request();
      const url = new URL(request.url());
      if (url.origin !== process.argv[2]) return route.abort();
      if (url.pathname.startsWith('/settings/backup')) assert.equal(url.search, '', 'secrets must not enter query strings');
      assert.ok(!request.url().includes(op.ID), 'capability must not enter URLs');
      assert.ok(!(request.headers().referer || '').includes(op.ID), 'capability must not enter referrers');
      if (request.method() !== 'POST') return route.continue();
      assert.equal(request.headers()['hx-request'], undefined, 'HTMX must not intercept backup forms');
      const body = new URLSearchParams(request.postData());
      switch (url.pathname) {
        case '/settings/backup/create':
          assert.equal(body.get('archive_password'), 'archive-secret');
          pending = initial.backup === 'queued' ? [{ ...op, Phase: 'encrypting' }, op] : [op];
          last = { ...op, Phase: initial.backup };
          return route.fulfill({ json: { ...op, Phase: initial.backup } });
        case '/settings/backup/status':
          assert.equal(request.headers()['x-backup-operation'], op.ID);
          statusCalls++;
          if (failNextStatus) {
            const failure = failNextStatus; failNextStatus = '';
            return failure === 'network' ? route.abort('failed') : route.fulfill({ status: 503, body: 'Temporarily unavailable' });
          }
          last = pending.shift() || last;
          return route.fulfill({ json: last });
        case '/settings/backup/download':
          assert.equal(body.get('current_password'), 'account-secret');
          assert.equal(body.get('operation'), op.ID);
          return route.fulfill({ headers: { 'Content-Type': 'application/octet-stream', 'Content-Disposition': 'attachment; filename="freefsm-backup.age"' }, body: 'encrypted-archive' });
        case '/settings/backup/upload':
          assert.equal(request.headers()['content-type'], 'application/octet-stream');
          assert.equal(JSON.parse(request.headers()['x-archive-password']), ' päss密 ');
          assert.equal(request.postData(), 'encrypted-archive');
          pending = initial.upload === 'queued' ? [{ ...op, Kind: 'upload', Phase: 'validating' }] : [];
          pending.push({ ...op, Kind: 'upload', Phase: 'ready' });
          last = { ...op, Kind: 'upload', Phase: initial.upload };
          return route.fulfill({ json: { ...op, Kind: 'upload', Phase: initial.upload } });
        case '/settings/backup/restore':
          assert.equal(body.get('current_password'), 'destination-password');
          assert.equal(body.get('operation'), op.ID);
          assert.equal(body.get('archive_digest'), op.ArchiveDigest);
          assert.equal(body.get('confirmation'), 'Destination');
          pending = [{ ...op, Kind: 'restore', Phase: 'complete' }];
          last = { ...op, Kind: 'restore', Phase: initial.restore };
          return route.fulfill({ json: { ...op, Kind: 'restore', Phase: initial.restore } });
        default: throw new Error('Unexpected POST ' + url.pathname);
      }
    });
    await page.goto(process.argv[2] + '/setup/company');
    assert.equal(await page.locator('.settings-tabs [data-tab=backup]').count(), 0);
    await page.goto(process.argv[2] + '/settings');
    for (const tab of ['company', 'email', 'pdf', 'map', 'security']) {
      await page.locator('.settings-tabs [data-tab=' + tab + ']').click();
      assert.equal(await page.locator('#tab-' + tab).isVisible(), true);
      assert.equal(await page.locator('.settings-tabs .active').getAttribute('data-tab'), tab);
      assert.equal(await page.locator('.settings-tabs [aria-current=true]').getAttribute('data-tab'), tab);
    }
    assert.equal(await page.locator('form[action="/settings"] form').count(), 0);
    assert.equal(await page.locator('form[action="/settings/enable-email"]').count(), 1);
    await page.locator('.settings-tabs [data-tab=backup]').click();
    await page.waitForURL('**/settings/backup');
    assert.equal(await page.title(), 'Settings - FreeFSM');
    assert.equal(await page.locator('.settings-tabs .active').getAttribute('data-tab'), 'backup');
    assert.equal(await page.locator('.settings-tabs [aria-current=true]').getAttribute('data-tab'), 'backup');
    assert.equal(await page.locator('#backup-create').evaluate(form => form.parentElement.closest('form')), null);
    await page.locator('.settings-tabs [data-tab=email]').click();
    await page.waitForURL('**/settings#tab-email');
    assert.equal(await page.locator('#tab-email').isVisible(), true);
    await page.locator('.settings-tabs [data-tab=backup]').click();
    await page.waitForURL('**/settings/backup');
    assert.equal(await page.locator('#backup-ui').getAttribute('hx-history'), 'false', 'HTMX must not persist the administrator DOM in its history cache');
    await page.locator('#backup-create input').fill('archive-secret');
    await page.locator('#backup-create button').click();
    await page.waitForFunction(() => sessionStorage.getItem('freefsm.backup.status'));
    assert.deepEqual(await page.evaluate(() => JSON.parse(sessionStorage.getItem('freefsm.backup.status'))), { id: op.ID, kind: 'backup' });
    if (initial.backup === 'encrypting') {
      failNextStatus = 'network';
      await page.reload();
      assert.equal(new URL(page.url()).pathname, '/settings/backup/status-page');
      assert.equal(await page.locator('form').count(), 0, 'status shell must have no mutation forms');
      await page.waitForFunction(() => document.getElementById('backup-progress').textContent.includes('Retrying'));
      await page.waitForFunction(() => document.getElementById('backup-progress').textContent.includes('Backup complete'));
      await page.goto(process.argv[2] + '/elsewhere');
      await page.goBack();
      await page.waitForFunction(() => document.getElementById('backup-progress').textContent.includes('Backup complete'));
      await page.locator('#backup-controls').click();
    }
    await page.waitForFunction(() => /complete/i.test(document.getElementById('backup-progress')?.textContent || ''));
    assert.equal(await page.locator('#backup-download').isVisible(), true, 'completed backup must offer download');
    assert.equal(await page.locator('#backup-progress a[href="/login"]').count(), 0, 'backup must not announce restore completion');
    assert.equal(await page.locator('#backup-review').isVisible(), false);
    assert.equal(statusCalls > 0, initial.backup !== 'complete');
    await page.locator('#backup-download [name=current_password]').fill('account-secret');
    const download = page.waitForEvent('download');
    await page.locator('#backup-download button').click();
    assert.equal((await download).suggestedFilename(), 'freefsm-backup.age');
    await page.locator('#backup-upload [name=archive]').setInputFiles({ name: 'archive.age', mimeType: 'application/octet-stream', buffer: Buffer.from('encrypted-archive') });
    await page.locator('#backup-upload [name=archive_password]').fill(' päss密 ');
    await page.locator('#backup-upload button').click();
    if (initial.upload !== 'ready') {
      await page.waitForFunction(() => /upload: (queued|validating)/.test(document.getElementById('backup-progress').textContent));
      assert.equal(await page.locator('#backup-review').isVisible(), false, 'unvalidated upload must not offer restore');
    }
    await page.locator('#backup-review').waitFor({ state: 'visible' });
    assert.equal(await page.locator('#backup-progress a[href="/login"]').count(), 0);
    assert.ok((await page.locator('#backup-metadata').textContent()).includes(op.SourceName));
    assert.ok((await page.locator('#backup-metadata').textContent()).includes('Source build: development'));
    assert.ok((await page.locator('body').textContent()).includes('Destination build: development'));
    assert.equal(await page.locator('#backup-metadata script').count(), 0);
    await page.locator('#backup-restore [name=current_password]').fill('destination-password');
    await page.locator('#backup-restore [name=confirmation]').fill('Destination');
    await page.locator('#backup-restore button').click();
    if (initial.restore === 'complete') await page.locator('#backup-ui[data-status-only]').waitFor();
    await page.waitForFunction(() => JSON.parse(sessionStorage.getItem('freefsm.backup.status')).kind === 'restore');
    assert.deepEqual(await page.evaluate(() => JSON.parse(sessionStorage.getItem('freefsm.backup.status'))), { id: op.ID, kind: 'restore' });
    if (initial.backup === 'encrypting') {
      failNextStatus = 'http';
      await page.reload();
      assert.equal(await page.locator('form').count(), 0);
      await page.waitForFunction(() => document.getElementById('backup-progress').textContent.includes('Retrying'));
    }
    await page.locator('#backup-progress a[href="/login"]').waitFor();
    assert.equal(await page.locator('#backup-download').isVisible(), false);
    assert.equal(await page.locator('#backup-review').isVisible(), false);
    assert.equal(await page.locator('form').count(), 0, 'restore completion must discard pre-restore administrator controls');
    if (initial.backup === 'encrypting') {
      // A terminal failure is fetched again on reload, not lost or auto-dismissed.
      pending = [];
      last = { ...op, Kind: 'restore', Phase: 'failed', Error: 'Operation failed during replacing-files.' };
      await page.reload();
      await page.waitForFunction(() => document.getElementById('backup-progress').textContent.includes('Operation failed'));
      await page.reload();
      await page.waitForFunction(() => document.getElementById('backup-progress').textContent.includes('Operation failed'));
      assert.deepEqual(await page.evaluate(() => Object.keys(sessionStorage)), ['freefsm.backup.status']);
      await page.locator('#backup-dismiss').click();
      assert.equal(await page.evaluate(() => sessionStorage.getItem('freefsm.backup.status')), null);
      await page.reload();
      assert.ok((await page.locator('#backup-progress').textContent()).includes('No saved operation'));
    }
    assert.deepEqual(errors, []);
    console.log('Backup/complete download, upload/ready review and restore/complete login passed:', initial);
    await page.close();
    }
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });
