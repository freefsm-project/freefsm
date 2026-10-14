const {chromium} = require('playwright-core');
const assert = require('node:assert/strict');
(async () => {
  const base = process.argv[2];
  const browser = await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium', headless:true, args:['--no-sandbox']});
  try {
    const context = await browser.newContext();
    await context.addCookies([{name:'session', value:process.env.PAIRING_BROWSER_COOKIE, url:base}]);
    const page = await context.newPage();
    page.setDefaultTimeout(12000);
    let bearer = '', requests = 0;
    const leaks = [];
    page.on('console', message => { if (bearer && message.text().includes(bearer)) leaks.push('console'); });
    page.on('request', request => {
      if (new URL(request.url()).pathname === '/api/v1/sync/jobs') {
        requests++;
        if (request.headers().cookie) leaks.push('API cookie');
      }
      if (bearer && request.url().includes(bearer)) leaks.push('URL');
    });
    await page.goto(base+'/api/v1/reference');
    await page.getByLabel('Device label').fill('Job sync reference tablet');
    const start = page.waitForResponse(r => new URL(r.url()).pathname === '/api/v1/pairings');
    await page.getByRole('button', {name:'Start pairing', exact:true}).click();
    const started = await (await start).json();
    const approval = await context.newPage();
    await approval.goto(base+'/pair');
    await approval.getByLabel('Pairing code').fill(started.data.user_code);
    await approval.getByRole('button', {name:'Review device', exact:true}).click();
    await approval.getByRole('heading', {name:'Job sync reference tablet', exact:true}).waitFor();
    await approval.getByRole('button', {name:'Approve device', exact:true}).click();
    await approval.getByText('Device approved.', {exact:false}).waitFor();
    const redeem = page.waitForResponse(r => new URL(r.url()).pathname === '/api/v1/pairings/redeem');
    await page.getByRole('button', {name:'Redeem approved pairing', exact:true}).click();
    bearer = (await (await redeem).json()).data.token;
    const state = () => page.locator('#sync-state').textContent();
    const records = async () => JSON.parse(await page.locator('#sync-records').textContent());
    const step = async id => {
      await page.locator('#'+id).click();
      await page.waitForFunction(() => !document.querySelector('#sync-start').disabled || document.querySelector('#start button').disabled === false);
    };
    const phase = async name => assert.equal((await context.request.post(base+'/__sync_fixture/'+name)).status(), 204);
    await page.getByLabel('Page size', {exact:true}).fill('1');
    await step('sync-start');
    assert.match(await state(), /incomplete; applied completion version 0; 1 Jobs/);
    assert.equal((await records())[0].title, 'Persisted sync Job 0');
    await phase('edit');
    await phase('remove'); // Includes an identity already delivered on a prior page.
    await page.getByLabel('Simulate next local application failure').check();
    await step('sync-next');
    assert.equal((await records()).length, 0, 'local failure rolled back authoritative removal');
    assert.match(await state(), /incomplete; applied completion version 0/);
    await step('sync-next');
    assert.equal((await records()).length, 1);
    assert.equal((await records())[0].title, 'Persisted sync Job 1', 'stable materialized page');
    await page.getByLabel('Simulate next local application failure').check();
    await step('sync-next');
    assert.match(await state(), /incomplete; applied completion version 0; 1 Jobs/);
    await step('sync-next');
    assert.match(await state(), /; complete; applied completion version 1; 2 Jobs/);
    const completed = await records();
    await step('sync-replay');
    assert.deepEqual(await records(), completed, 'replayed completion appended or resurrected data');
    assert.match(await state(), /completion version 1/);
    await step('sync-delta');
    assert.ok((await records()).some(j => j.title === 'Concurrent persisted edit'), 'missed edit committed during pagination');
    assert.match(await state(), /; complete; applied completion version 2; 2 Jobs/);
    const deltaRecords = await records();
    await step('sync-replay');
    assert.deepEqual(await records(), deltaRecords, 'delta replay duplicated local identity/history');
    assert.match(await state(), /; complete; applied completion version 2; 2 Jobs/);

    // Real transport failure and invalid JSON leave the saved cursor unchanged.
    await page.route('**/api/v1/sync/jobs?*', route => route.abort('failed'), {times:1});
    await step('sync-delta');
    assert.match(await state(), /incomplete; applied completion version 2/);
    await page.route('**/api/v1/sync/jobs?*', route => route.fulfill({status:200, contentType:'application/json', body:'{broken'}), {times:1});
    await step('sync-next');
    assert.match(await state(), /incomplete; applied completion version 2/);
    await step('sync-next');
    assert.match(await state(), /; complete;/);

    // Additive responses are ignored; unknown enum values cannot drive behavior.
    await page.route('**/api/v1/sync/jobs?*', async route => {
      const response = await route.fetch();
      const body = await response.json();
      body.future = 'UNKNOWN-RESPONSE-FIELD';
      for (const j of body.data.records) { j.future = 'UNKNOWN-RECORD-FIELD'; j.status.category = 'future_category'; }
      await route.fulfill({response, json:body});
    }, {times:1});
    await page.getByLabel('Page size', {exact:true}).fill('50');
    await step('sync-start');
    assert.equal((await records())[0].status.category, 'unknown');
    assert.ok(!(await page.content()).includes('UNKNOWN-'));
    await page.route('**/api/v1/sync/jobs?*', async route => {
      const response = await route.fetch(); const body = await response.json();
      body.data.purge = 'future_purge_instruction'; await route.fulfill({response, json:body});
    }, {times:1});
    await step('sync-delta');
    assert.deepEqual(await records(), [], 'unknown purge instruction retained context');
    assert.equal(await page.locator('#sync-next').isDisabled(), true);
    await step('sync-start');
    await phase('grants');
    await step('sync-delta');
    assert.deepEqual(await records(), [], 'resnapshot did not purge cached context');
    assert.match(await page.locator('#status').textContent(), /resnapshot_required/);
    await step('sync-start');
    assert.equal((await records()).length, 2);
    assert.ok((await records()).every(j => !j.customer), 'revoked facet retained');
    assert.ok(!(await page.content()).includes(bearer), 'bearer in DOM');
    assert.deepEqual(await page.evaluate(() => ({local:localStorage.length, session:sessionStorage.length})), {local:0, session:0});
    assert.equal(await page.evaluate(async () => (await navigator.serviceWorker.getRegistrations()).length), 0);
    assert.ok(!(await page.content()).includes('PRIVATE-INTERNAL'));
    await phase('revoke');
    // Even a damaged denial must purge before JSON parsing.
    await page.route('**/api/v1/sync/jobs?*', async route => {
      const response = await route.fetch(); assert.equal(response.status(), 401);
      await route.fulfill({response, body:'not json'});
    }, {times:1});
    await step('sync-delta');
    assert.deepEqual(await records(), []);
    assert.equal(await page.locator('#sync-start').isDisabled(), true);
    const contacted = requests;
    await page.waitForTimeout(100);
    assert.equal(requests, contacted, 'automatic synchronization/retry');
    assert.deepEqual(leaks, []);
    // A broad paired user can read Jobs without acquiring unassigned master context.
    await context.addCookies([{name:'session', value:process.env.SYNC_ADMIN_BROWSER_COOKIE, url:base}]);
    await page.reload();
    await page.getByLabel('Device label').fill('All-scope sync tablet');
    const broadStart = page.waitForResponse(r => new URL(r.url()).pathname === '/api/v1/pairings');
    await page.getByRole('button', {name:'Start pairing', exact:true}).click();
    const broadCode = (await (await broadStart).json()).data.user_code;
    await approval.goto(base+'/pair');
    await approval.getByLabel('Pairing code').fill(broadCode);
    await approval.getByRole('button', {name:'Review device', exact:true}).click();
    await approval.getByRole('heading', {name:'All-scope sync tablet', exact:true}).waitFor();
    await approval.getByRole('button', {name:'Approve device', exact:true}).click();
    await approval.getByText('Device approved.', {exact:false}).waitFor();
    const broadRedeem = page.waitForResponse(r => new URL(r.url()).pathname === '/api/v1/pairings/redeem');
    await page.getByRole('button', {name:'Redeem approved pairing', exact:true}).click();
    bearer = (await (await broadRedeem).json()).data.token;
    await page.getByLabel('Job scope', {exact:true}).selectOption('all');
    await step('sync-start');
    assert.match(await state(), /; complete;/);
    assert.equal((await records()).length, 3);
    assert.ok((await records()).every(j => !j.customer && Object.keys(j.masters).length === 0));
    assert.ok(!(await page.content()).includes(bearer));
    assert.deepEqual(leaks, []);
    console.log('PASS paired persisted Job snapshot/delta, concurrent edit, prior-page removal, atomic local failure/retry, replay, network/JSON failures, additive fields/enums, resnapshot/facet purge and malformed unauthorized purge; no bearer/storage leaks');
  } finally { await browser.close(); }
})().catch(error => { console.error(String(error.stack || error).replace(/[a-f0-9]{64}/gi, '[redacted]').replaceAll(process.env.PAIRING_BROWSER_COOKIE || 'no-cookie', '[redacted]').replaceAll(process.env.SYNC_ADMIN_BROWSER_COOKIE || 'no-admin-cookie', '[redacted]')); process.exit(1); });
