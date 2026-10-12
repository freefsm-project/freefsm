const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright-core');

(async () => {
  const [base, token, parent] = process.argv.slice(2);
  const files = ['document.doc', 'workbook.xls', 'slides.ppt'].map(name => ({name, mimeType:'application/octet-stream', buffer:fs.readFileSync(path.join(__dirname,'../files/legacy-office',name))}));
  const browser = await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium-browser',headless:true,args:['--no-sandbox']});
  try {
    const context = await browser.newContext();
    await context.addCookies([{name:'session',value:token,url:base,httpOnly:true}]);
    const page = await context.newPage();
    await page.goto(`${base}/customers/${parent}`);
    let requests = 0;
    const publications=[];
    page.on('request',r => {if (r.method()==='POST' && new URL(r.url()).pathname==='/files') {requests++;publications.push(r.postData()?.match(/name="publication_id"\r\n\r\n([^\r]+)/)?.[1] || 'no publication identity');}});
    // A rejected oversized batch sends no request, even with valid file bytes.
    await page.locator('input[type=file][name=file]').setInputFiles(Array.from({length:11},(_,i) => ({...files[0],name:`document-${i}.doc`})));
    await page.locator('.files-upload-error').filter({hasText:'up to 10 files'}).waitFor();
    assert.equal(requests,0);
    let loseResponse = true;
    await page.route('**/files?**',async route => {
      if (!loseResponse) return route.continue();
      loseResponse=false;
      const response=await route.fetch();
      assert.equal(response.status(),200,await response.text());
      await route.abort('failed');
    });
    await page.locator('input[type=file][name=file]').setInputFiles(files);
    await page.locator('.files-drop-form button[type=submit]').click();
    await page.locator('.files-upload-summary').filter({hasText:'2 uploaded; 1 failed'}).waitFor();
    assert.equal(await page.locator('.files-upload-uploaded').count(),2);
    await page.getByRole('button',{name:'Retry failed files',exact:true}).click();
    await page.locator('.files-thumb[data-file-name="slides.ppt"]').waitFor();
    assert.equal(requests,4,'only the lost-response item should be retried: '+JSON.stringify(publications));
    for (const file of files) {
      const tile=page.locator(`.files-thumb[data-file-name="${file.name}"]`);
      const url=await tile.getAttribute('data-file-url');
      const download=await page.request.get(base+url);
      assert.equal(download.status(),200);
      assert.deepEqual(await download.body(),file.buffer);
    }
    // A valid DOC under an XLS extension fails without disturbing successes.
    await page.locator('input[type=file][name=file]').setInputFiles({...files[0],name:'mismatched.xls'});
    await page.locator('.files-drop-form button[type=submit]').click();
    await page.locator('.files-upload-failed').filter({hasText:'Invalid file type'}).waitFor();
    assert.equal(await page.locator('.files-thumb').count(),3);
    await page.reload();
    for (const file of files) {
      const tile=page.locator(`.files-thumb[data-file-name="${file.name}"]`);
      const url=await tile.getAttribute('data-file-url');
      await tile.click();
      await page.locator('.files-preview-fallback').waitFor();
      assert.equal(await page.locator('#files-preview-body img, #files-preview-body iframe').count(),0);
      await page.getByRole('button',{name:'Rename',exact:true}).click();
      const renamed=`renamed-${file.name}`;
      await page.locator('#files-preview-rename-form [name=original_name]').fill(renamed);
      await page.locator('#files-preview-rename-form button[type=submit]').click();
      await page.locator(`.files-thumb[data-file-name="${renamed}"]`).waitFor();
      const download=await page.request.get(base+url);
      assert.deepEqual(await download.body(),file.buffer,'rename must preserve bytes and identity');
      await page.locator(`.files-thumb[data-file-name="${renamed}"]`).click();
      page.once('dialog',d => d.accept());
      await page.getByRole('button',{name:'Remove',exact:true}).click();
      await page.waitForFunction(name => !document.querySelector(`.files-thumb[data-file-name="${name}"]`),renamed);
      assert.equal((await page.request.get(base+url)).status(),404);
    }
    console.log('Real DOC/XLS/PPT persisted batch: 11-file rejection, mixed-success lost-response retry once, mismatch denial, exact downloads, stable-identity rename and removal passed.');
  } finally {await browser.close();}
})().catch(error => {console.error(error);process.exitCode=1;});
