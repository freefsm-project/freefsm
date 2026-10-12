const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');

// Called by the real Backup HTTP/engine fixture. The transport is cut only AFTER
// production handling has accepted the actual encrypted backup/upload.
(async () => {
  const [base, cookie] = process.argv.slice(2);
  const browser = await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium-browser',args:['--no-sandbox']});
  try {
    const context=await browser.newContext({acceptDownloads:true});
    await context.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
    const page=await context.newPage();
    const requests={create:0,upload:0,restore:0};const identities={};
    for(const kind of ['create','upload']) await page.route('**/settings/backup/'+kind,async route=>{
      requests[kind]++;
      identities[kind]=route.request().headers()['x-backup-action'];
      assert.match(identities[kind] || '',/^[0-9a-f]{64}$/);
      assert.equal(await page.evaluate(()=>JSON.parse(sessionStorage.getItem('freefsm.backup.status')).action),identities[kind],'persist opaque identity before transfer');
      const response=await route.fetch();
      assert.ok(response.ok(),`real ${kind} acceptance ${response.status()}: ${(await response.text()).slice(0,300)}`);
      await route.abort('failed');
    });
    await page.route('**/settings/backup/restore',route=>{requests.restore++;return route.abort();});
    assert.equal((await page.goto(base+'/settings/backup')).status(),200);
    await page.locator('#backup-create [name=archive_password]').fill('archive-secret');
    await page.locator('#backup-create button').click();
    await page.locator('#backup-download').waitFor({state:'visible',timeout:90000});
    assert.equal(requests.create,1,'status reconciliation never starts another real backup');
    await page.locator('#backup-download [name=current_password]').fill('account-0');
    const downloadEvent=page.waitForEvent('download');
    await page.locator('#backup-download button').click();
    const download=await downloadEvent;
    assert.equal(await download.failure(),null);
    await page.locator('#backup-upload [name=archive]').setInputFiles(await download.path());
    await page.locator('#backup-upload [name=archive_password]').fill('archive-secret');
    await page.locator('#backup-upload button').click();
    await page.locator('#backup-review').waitFor({state:'visible',timeout:90000});
    assert.equal(requests.upload,1,'lost upload acceptance is queried, not reuploaded');
    assert.notEqual(identities.create,identities.upload,'different deliberate initiation has its own identity');
    const saved=await page.evaluate(()=>JSON.parse(sessionStorage.getItem('freefsm.backup.status')));
    assert.deepEqual(Object.keys(saved).sort(),['id','kind']);assert.equal(saved.kind,'upload');
    await page.reload();
    assert.equal(await page.locator('form').count(),0,'recovery shell does not restore privileged input');
    await page.getByText('Upload validated. Return to administrator controls to review and confirm restore.',{exact:true}).waitFor();
    await page.locator('#backup-controls').click();
    await page.locator('#backup-review').waitFor({state:'visible'});
    assert.equal(requests.restore,0,'a recovered upload never automatically restores');
    console.log('PASS real encrypted Backup create/download/upload validation with both initial responses discarded, original-capability reconciliation, no duplicate initiation, durable status-shell resume and no automatic Restore');
  } finally { await browser.close(); }
})().catch(error=>{console.error(error);process.exitCode=1;});
