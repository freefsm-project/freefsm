'use strict';
const assert = require('node:assert/strict');
const {chromium} = require('playwright-core');
const [base, cookie, job] = process.argv.slice(2);
(async () => {
  const browser = await chromium.launch({headless:true, executablePath:process.env.BROWSER_EXECUTABLE, args:['--no-sandbox']});
  try {
    const context = await browser.newContext();
    await context.addCookies([{name:'session', value:cookie, url:base}]);
    const page = await context.newPage();
    page.setDefaultTimeout(10000);
    await page.goto(`${base}/jobs/${job}`);
    await page.getByRole('link', {name:'View checklist and history'}).click();
    await page.getByLabel('New Subtask title').fill('First maintenance entry');
    await Promise.all([page.waitForEvent('load'), page.getByRole('button', {name:'Add Subtask', exact:true}).click()]);
    assert.equal(await page.getByRole('button', {name:'Complete', exact:true}).count(), 0);
    page.once('dialog', d => d.accept());
    await Promise.all([page.waitForEvent('load'), page.getByRole('button', {name:'Remove Subtask'}).click()]);
    await page.getByRole('link', {name:'Back to Job', exact:true}).click();
    await page.getByRole('link', {name:'View checklist and history'}).click();
    await page.getByText('No Subtasks.', {exact:true}).waitFor();
    assert.match(await page.locator('body').innerText(), /First maintenance entry/);
    console.log('empty navigation and retained history passed');
    for (const scenario of ['committed', 'unreceived', 'scope-loss']) {
      const committed = scenario !== 'unreceived';
      const title = `Original ${scenario} add`;
      await page.getByLabel('New Subtask title').fill(title);
      const original = await page.locator('form').filter({has:page.getByRole('button', {name:'Add Subtask', exact:true})}).locator('[name=operation_id]').inputValue();
      await page.route('**/subtask-operations/*', route => route.abort('connectionclosed'));
      const cdp = await context.newCDPSession(page);
      const posts = [];
      page.on('request', capture);
      function capture(request) { if (request.method()==='POST' && request.url()===`${base}/jobs/${job}/subtasks`) posts.push(new URLSearchParams(request.postData())); }
      let lost = false;
      const interrupted = new Promise((resolve, reject) => {
        cdp.on('Fetch.requestPaused', event => {
          (async () => {
            if (event.request.method==='POST' && !lost) {
              lost = true;
              if (committed) assert.equal(event.responseStatusCode, 200);
              await cdp.send('Fetch.failRequest', {requestId:event.requestId, errorReason:'ConnectionClosed'});
              resolve();
            } else await cdp.send(committed ? 'Fetch.continueResponse' : 'Fetch.continueRequest', {requestId:event.requestId});
          })().catch(reject);
        });
      });
      await cdp.send('Fetch.enable', {patterns:[{urlPattern:`${base}/jobs/${job}/subtasks`, requestStage:committed?'Response':'Request'}]});
      await page.getByRole('button', {name:'Add Subtask', exact:true}).click();
      await interrupted;
      await page.getByRole('button', {name:'Check original operation', exact:true}).waitFor();
      await cdp.send('Fetch.disable'); await cdp.detach();
      await page.reload();
      await page.getByRole('button', {name:'Check original operation', exact:true}).waitFor();
      assert.equal(await page.getByRole('button', {name:'Add Subtask', exact:true}).isDisabled(), true);
      assert.match(await page.locator('#subtask-outcome').innerText(), new RegExp(original));
      await page.unroute('**/subtask-operations/*');
      if (scenario === 'scope-loss') assert.equal((await page.request.post(`${base}/test-ui-revoke`)).status(), 204);
      await page.getByRole('button', {name:'Check original operation', exact:true}).click();
      if (committed) {
        await page.getByText('Original operation: accepted. Reload to inspect current authorized checklist.', {exact:true}).waitFor();
        if (scenario === 'scope-loss') {
          assert.equal(await page.getByRole('button', {name:'Retry unchanged original operation', exact:true}).count(), 0);
          assert.equal(await page.locator('[data-subtask-id]').count(), 0);
          assert.equal((await page.locator('body').innerText()).includes(title), false);
          assert.equal(await page.evaluate(title => JSON.stringify(sessionStorage).includes(title), title), false);
          assert.equal(posts.length, 1);
          page.off('request', capture);
          continue;
        }
        await page.reload();
        assert.equal(posts.length, 1, 'checking committed receipt must not resubmit');
      } else {
        await page.getByText('No terminal receipt available. Outcome remains unknown.', {exact:false}).waitFor();
        await Promise.all([page.waitForEvent('load'), page.getByRole('button', {name:'Retry unchanged original operation', exact:true}).click()]);
        assert.equal(posts.length, 2);
      }
      for (const body of posts) {
        assert.equal(body.get('operation_id'), original);
        assert.equal(body.get('title'), title);
        assert.equal(body.get('action'), 'add');
        const intent = new URLSearchParams(body); intent.delete('csrf_token');
        const originalIntent = new URLSearchParams(posts[0]); originalIntent.delete('csrf_token');
        assert.deepEqual([...intent], [...originalIntent], 'all original intent fields retained');
      }
      page.off('request', capture);
      assert.equal(await page.locator('[data-subtask-id]').filter({has:page.getByText(title, {exact:true})}).count(), 1);
      const history = page.locator('h2').filter({hasText:'Checklist history'}).locator('+ ol > li');
      assert.equal(await history.filter({hasText:title}).count(), 1, 'exactly one persisted history event');
    }
    console.log('committed and unreceived double-loss recovery passed');
  } finally { await browser.close(); }
})().catch(error => {console.error(error); process.exitCode=1;});
