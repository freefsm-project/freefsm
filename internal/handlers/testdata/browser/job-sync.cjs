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
    // Every tokenless request is a NEW materialization, including replay/retry.
    await phase('only-first');
    await step('sync-start');
    assert.match(await state(), /; complete;/);
    const original = (await records())[0];
    assert.equal(original.title, 'Persisted sync Job 0');
    assert.equal(original.customer.display_name, 'Browser sync Customer');
    await phase('remove');
    await page.route('**/api/v1/sync/jobs?*', async route => {
      const response = await route.fetch();
      assert.equal(response.status(), 200);
      assert.deepEqual(await records(), [], 'old context must disappear before fresh response completion');
      assert.match(await state(), /incomplete; applied completion version 1/);
      await route.fulfill({response});
    }, {times:1});
    const emptyResponse = page.waitForResponse(r => new URL(r.url()).pathname === '/api/v1/sync/jobs');
    await step('sync-replay');
    const empty = await (await emptyResponse).json();
    assert.deepEqual(empty.data.records, []);
    assert.deepEqual(empty.data.removed, [], 'fresh snapshot has no old baseline');
    assert.deepEqual(await records(), [], 'tokenless replay retained inaccessible Job/context');
    assert.match(await state(), /; complete; applied completion version 2; 0 Jobs/);
    await step('sync-delta');
    assert.deepEqual(await records(), [], 'empty snapshot completion retained old context');
    assert.match(await state(), /; complete; applied completion version 3; 0 Jobs/);

    for (const failure of ['none', 'network', 'application']) {
      await phase('restore-all');
      await page.getByLabel('Page size', {exact:true}).fill('1');
      await step('sync-start');
      assert.deepEqual(await records(), [original]);
      assert.match(await state(), /incomplete; applied completion version 0/);
      await phase('remove');
      if (failure === 'network') await page.route('**/api/v1/sync/jobs?*', route => route.abort('failed'), {times:1});
      if (failure === 'application') await page.getByLabel('Simulate next local application failure').check();
      await step('sync-replay');
      if (failure !== 'none') {
        assert.match(await state(), /incomplete; applied completion version 0/);
        assert.deepEqual(await records(), [], 'failed tokenless replay kept prior staging');
        await step('sync-next');
      }
      assert.equal((await records()).length, 1, 'initial-page replay mixed materializations');
      assert.equal((await records())[0].title, 'Persisted sync Job 1');
      await step('sync-next');
      assert.match(await state(), /; complete; applied completion version 1; 2 Jobs/);
      assert.ok((await records()).every(j => j.id !== original.id));
    }
    console.log('PASS tokenless completed/first-incomplete-page replay and network/local-application retries replace old materialization');

    // Create a successor before observing loss, then prove neither old lineage
    // nor a failed local application can suppress unchanged A after reassignment.
    await phase('restore-all');
    const initialResponse = page.waitForResponse(r => new URL(r.url()).pathname === '/api/v1/sync/jobs');
    await step('sync-start');
    const initial = (await (await initialResponse).json()).data;
    const query = (key, token) => base+'/api/v1/sync/jobs?scope=assigned&page_size=1&'+key+'='+encodeURIComponent(token);
    const apiRead = async url => {
      const response = await context.request.get(url, {headers:{Authorization:'Bearer '+bearer, Cookie:''}});
      assert.equal(response.status(), 200);
      return (await response.json()).data;
    };
    let ahead = initial;
    while (!ahead.complete) ahead = await apiRead(query('page', ahead.next_page));
    const successorURL = query('cursor', ahead.completion_cursor);
    const successor = await apiRead(successorURL);
    assert.equal(successor.complete, true);
    await phase('remove');
    await page.getByLabel('Simulate next local application failure').check();
    await page.route('**/api/v1/sync/jobs?*', async route => {
      const response = await route.fetch();
      assert.equal(response.status(), 409);
      assert.equal((await response.json()).error.code, 'resnapshot_required');
      // Local decoding fails after receipt of the authoritative purge status.
      await route.fulfill({response, body:'{broken'});
    }, {times:1});
    await step('sync-next');
    assert.deepEqual(await records(), [], 'scope loss must purge already delivered A despite local failure');
    assert.match(await page.locator('#status').textContent(), /Synchronization incomplete/);
    assert.match(await state(), /incomplete; applied completion version 0/);
    assert.equal(await page.locator('#sync-next').isDisabled(), true);
    await phase('restore'); // No representation edits: exact original A must return.
    // Retry old page through the actual browser/client, even after scope returns.
    for (const stale of [query('page', initial.next_page), successorURL, query('cursor', successor.completion_cursor)]) {
      await page.route('**/api/v1/sync/jobs?*', route => route.continue({url:stale}), {times:1});
      await step('sync-start');
      assert.deepEqual(await records(), []);
      assert.match(await page.locator('#status').textContent(), /resnapshot_required/);
      assert.match(await state(), /incomplete; applied completion version 0/);
    }
    // The failure toggle remains pending through denials; a fresh successful
    // response now fails local application and must be explicitly retried.
    await step('sync-start');
    assert.match(await state(), /incomplete; applied completion version 0/);
    assert.deepEqual(await records(), []);
    await step('sync-next');
    assert.deepEqual(await records(), [original]);
    await step('sync-next');
    await step('sync-next');
    assert.match(await state(), /; complete; applied completion version 1; 3 Jobs/);
    assert.deepEqual((await records()).find(j => j.id === original.id), original);
    await step('sync-delta');
    assert.match(await state(), /; complete; applied completion version 2; 3 Jobs/);
    assert.deepEqual((await records()).find(j => j.id === original.id), original);
    console.log('PASS delivered scope-loss purge despite local failure, unchanged scope return, old-page and already-created successor invalidation, fresh retry/completion/delta restoration');

    await page.getByLabel('Page size', {exact:true}).fill('1');
    await step('sync-start');
    assert.match(await state(), /incomplete; applied completion version 0; 1 Jobs/);
    assert.equal((await records())[0].title, 'Persisted sync Job 0');
    await phase('edit');
    await page.getByLabel('Simulate next local application failure').check();
    await step('sync-next');
    assert.equal((await records()).length, 1, 'local failure partially applied a page');
    assert.match(await state(), /incomplete; applied completion version 0/);
    await step('sync-next');
    assert.equal((await records()).length, 2);
    assert.equal((await records())[1].title, 'Persisted sync Job 1', 'stable materialized page');
    await page.getByLabel('Simulate next local application failure').check();
    await step('sync-next');
    assert.match(await state(), /incomplete; applied completion version 0; 2 Jobs/);
    await step('sync-next');
    assert.match(await state(), /; complete; applied completion version 1; 3 Jobs/);
    const completed = await records();
    await step('sync-replay');
    assert.deepEqual(await records(), completed, 'replayed completion appended or resurrected data');
    assert.match(await state(), /completion version 1/);
    await step('sync-delta');
    assert.ok((await records()).some(j => j.title === 'Concurrent persisted edit'), 'missed edit committed during pagination');
    assert.match(await state(), /; complete; applied completion version 2; 3 Jobs/);
    const deltaRecords = await records();
    await step('sync-replay');
    assert.deepEqual(await records(), deltaRecords, 'delta replay duplicated local identity/history');
    assert.match(await state(), /; complete; applied completion version 2; 3 Jobs/);

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
    await phase('remove');
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
