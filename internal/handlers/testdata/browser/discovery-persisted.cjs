const assert = require('node:assert/strict');
const path = require('node:path');
const { chromium } = require('playwright-core');

(async () => {
  const [base, cookie, otherCookie, changedID, profile, artifacts, childJSON] = process.argv.slice(2);
  const children = JSON.parse(childJSON);
  const context = await chromium.launchPersistentContext(profile, {
    executablePath: process.env.BROWSER_EXECUTABLE, headless: true,
    args: ['--no-sandbox'], viewport: { width: 1280, height: 900 },
    reducedMotion: 'reduce'
  });
  console.log('Browser: '+context.browser().version());
  await context.tracing.start({ screenshots: true, snapshots: true });
  const page = await context.newPage();
  const remembered = async p => {
    await p.locator('[data-list-save-status]').filter({hasText:'Filters remembered'}).waitFor();
  };
  const rows = p => p.locator('[data-discovery-results] [data-record-id]');
  try {
    await context.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
    await page.goto(base+'/search?q=Browser106&type=item&sort=name&per_page=25&page=2');
    await remembered(page);
    assert.equal(await rows(page).count(),25);
    assert.match(await rows(page).first().innerText(),/Browser106 025/);
    const shared = page.url();
    const resetRevision = await page.locator('[data-list-reset] [name=revision]').inputValue();
    // The same real session cannot write preferences with omitted/forged CSRF.
    for (const csrf of ['', 'forged106']) {
      const response = await context.request.post(base+'/search/state', {
        form: {query:'q=forged106',revision:resetRevision,csrf_token:csrf},
        headers:{'X-List-State':'save'}
      });
      assert.equal(response.status(),400,'production nosurf boundary must deny preference write');
    }
    const destination = rows(page).nth(15).getByRole('link').first();
    await destination.scrollIntoViewIfNeeded();
    const scroll = await page.evaluate(()=>scrollY);
    await destination.click();
    await page.waitForURL(/\/items\/\d+$/);
    assert.equal((await context.request.post(base+'/fixture/change')).status(),204);
    await page.goBack();
    await page.waitForURL(shared);
    await remembered(page);
    await page.waitForFunction(y=>Math.abs(scrollY-y)<8,scroll);
    assert.match(await page.locator(`[data-record-id="${changedID}"]`).innerText(),/Fresh persisted description106/);
    console.log('PASS Back restored page/sort/scroll and fresh persisted data; CSRF denials');

    // A fresh context authenticates through the production login form.
    const fresh = await context.browser().newContext();
    const next = await fresh.newPage();
    await next.goto(base+'/login');
    await next.locator('input[name=email]').fill('Browser106@discovery.test');
    await next.locator('input[name=password]').fill('PermissionFixture1!');
    await next.locator('form').filter({has:next.locator('input[name=password]')}).getByRole('button',{name:/sign in|log in/i}).click();
    await next.waitForURL(url=>url.pathname!='/login');
    await next.goto(base+'/search');
    await remembered(next);
    assert.equal(await next.getByLabel('Search records',{exact:true}).inputValue(),'Browser106');
    assert.equal(await next.getByLabel(/^Sort/).inputValue(),'name');
    assert.equal(new URL(next.url()).searchParams.get('page'),'1','page is history, not durable preference');
    await next.goto(base+'/search?q=Separate106');
    await remembered(next);
    assert.equal(await rows(next).count(),1);
    assert.match(await rows(next).innerText(),/Separate106 customer/,'whole URL override cleared saved Item filter');
    assert.equal(await next.locator('input[name=type]:checked').count(),0);
    await next.getByRole('button',{name:'Reset filters',exact:true}).click();
    await remembered(next);
    assert.equal(await next.getByLabel('Search records',{exact:true}).inputValue(),'');
    await next.goto(base+'/preferences');
    await next.goto(base+'/search');
    await remembered(next);
    assert.equal(await next.getByLabel('Search records',{exact:true}).inputValue(),'');
    await fresh.close();
    console.log('PASS new authenticated context restored server state, URL whole override and durable Reset');

    const isolated = await context.browser().newContext();
    await isolated.addCookies([{name:'session',value:otherCookie,url:base,httpOnly:true}]);
    const other = await isolated.newPage();
    await other.goto(base+'/search');
    await remembered(other);
    assert.equal(await other.getByLabel('Search records',{exact:true}).inputValue(),'');
    await isolated.close();

    for (const [key, term, extra] of [
      ['customers','Separate106',''],
      ['items','Browser106',''],
      ['leases','BrowserLease106','&lifecycle=draft']
    ]) {
      await page.goto(`${base}/${key}?search=${term}&per_page=50${extra}`);
      await remembered(page);
      assert.match(await page.locator('#main-content').innerText(),new RegExp(term));
      let persisted = new URLSearchParams(await page.locator('[data-list-save] [name=query]').inputValue());
      assert.equal(persisted.get('per_page'),'50');
      if (key==='leases') assert.equal(persisted.get('lifecycle'),'draft');
      await page.goto(base+'/preferences');
      await page.goto(`${base}/${key}`);
      await remembered(page);
      persisted = new URLSearchParams(await page.locator('[data-list-save] [name=query]').inputValue());
      assert.equal(persisted.get('search') || persisted.get('q'),term);
      assert.equal(persisted.get('per_page'),'50');
      await page.getByRole('button',{name:'Reset filters',exact:true}).click();
      await remembered(page);
      persisted = new URLSearchParams(await page.locator('[data-list-save] [name=query]').inputValue());
      assert.equal(persisted.get('search') || persisted.get('q') || '', '');
      assert.equal(persisted.get('per_page'),'25');
    }
    console.log('PASS Customer, Item and Lease persisted dedicated state, domain filter and Reset');

    for (const key of ['contacts','locations']) {
      const identities = async () => page.locator(`#main-content a[href^="/${key}/"]`).evaluateAll(
        (links, key) => [...new Set(links.map(a=>a.getAttribute('href')).filter(h=>new RegExp(`^/${key}/[0-9]+$`).test(h)))], key);
      await page.goto(`${base}/${key}?search=ChildBrowser106&sort=name&per_page=25&page=1`);
      await remembered(page);
      assert.deepEqual(await identities(),children[key].slice(0,25));
      await page.locator('.pagination-nav').getByRole('link',{name:/Next/}).click();
      await page.waitForURL(url=>url.pathname===`/${key}`&&url.searchParams.get('page')==='2');
      await remembered(page);
      assert.deepEqual(await identities(),children[key].slice(25));
      await page.locator('.pagination-nav').getByRole('link',{name:/Previous/}).click();
      await page.waitForURL(url=>url.pathname===`/${key}`&&url.searchParams.get('page')==='1');
      await remembered(page);
      assert.deepEqual(await identities(),children[key].slice(0,25));
      await page.goBack();
      await page.waitForURL(url=>url.pathname===`/${key}`&&url.searchParams.get('page')==='2');
      await remembered(page);
      assert.deepEqual(await identities(),children[key].slice(25));
    }
    console.log('PASS Contact and Location actual Next/Previous/Back identities across persisted pages');

    await page.goto(base+'/items?search=NoMatchBrowser106');
    await remembered(page);
    const itemResults = () => page.locator('#item-list-content .focused-list-results');
    const noMatch = await itemResults().innerText();
    assert.match(noMatch,/no .*match|no matching/i);
    assert.doesNotMatch(noMatch,/create your first/i);
    assert.equal((await context.request.post(base+'/fixture/hidden-item')).status(),204);
    await page.reload();
    await remembered(page);
    assert.equal(await itemResults().innerText(),noMatch,'hidden matching Item must not alter no-match wording or controls');
    await page.locator('#item-list-content').getByRole('link',{name:/clear filters/i}).click();
    await remembered(page);
    assert.equal(await page.locator('#item-list-content a.work-queue-title').count(),25);
    assert.match(await itemResults().innerText(),/Browser106/);
    assert.equal((await context.request.post(base+'/fixture/items-visibility?archived=1')).status(),204);
    await page.goto(base+'/items?q=');
    await remembered(page);
    const empty = await itemResults().innerText();
    assert.notEqual(empty,noMatch,'empty authorized scope differs from filtered no-match');
    assert.match(empty,/no items|no .*records|no .*yet/i);
    assert.match(empty,/create your first item/i);
    assert.equal(await page.locator('#item-list-content a.work-queue-title').count(),0);
    assert.equal((await context.request.post(base+'/fixture/hidden-item')).status(),204);
    await page.reload();
    await remembered(page);
    assert.equal(await itemResults().innerText(),empty,'hidden Items must not alter empty-authorized-scope wording');
    for (const key of ['q','search']) {
      await page.goto(`${base}/items?${key}=0`);
      await remembered(page);
      const literalZero = await itemResults().innerText();
      assert.equal(await itemResults().locator('[data-list-empty="no-matches"]').count(),1,'literal text 0 is an active filter');
      assert.match(literalZero,/no match(?:es|ing)/i);
      assert.doesNotMatch(literalZero,/create your first/i);
      await page.locator('#item-list-content').getByRole('link',{name:/clear filters/i}).click();
      await remembered(page);
      assert.equal(await itemResults().innerText(),empty,'clearing literal 0 restores empty-scope wording');
    }
    console.log('PASS literal q=0 and search=0 no-match wording and clear-filter recovery');
    assert.equal((await context.request.post(base+'/fixture/items-visibility?archived=0')).status(),204);
    console.log('PASS Item no-match/clear-filter versus empty authorized scope; hidden-record noninfluence');

    await page.goto(base+'/search?q=Browser106&type=item');
    await remembered(page);
    await page.setViewportSize({width:320,height:640});
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),'320px document reflow');
    await page.getByLabel('Search records',{exact:true}).focus();
    await page.keyboard.press('Tab');
    assert.ok(await page.evaluate(()=>document.activeElement!==document.body),'keyboard focus reaches controls');
    await page.screenshot({path:path.join(artifacts,'mobile.png'),fullPage:true});
    await page.setViewportSize({width:1280,height:900});
    const touch = await context.browser().newContext({viewport:{width:320,height:640},hasTouch:true,isMobile:true});
    await touch.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
    const phone = await touch.newPage();
    await phone.goto(base+'/search?q=Browser106&type=item');
    await remembered(phone);
    assert.equal(await phone.evaluate(()=>matchMedia('(pointer:coarse)').matches),true);
    assert.ok(await phone.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),'coarse-pointer reflow');
    const target = await phone.getByRole('button',{name:'Search',exact:true}).boundingBox();
    assert.ok(target.width>=44 && target.height>=44,'coarse Search target');
    await phone.screenshot({path:path.join(artifacts,'coarse-pointer.png'),fullPage:true});
    await touch.close();

    // Delay a genuine first response, then complete a second authenticated
    // search. A cancelled older XHR is an acceptable latest-request-wins policy.
    let releaseOld, observedOld;
    const release = new Promise(resolve => { releaseOld = resolve; });
    const observed = new Promise(resolve => { observedOld = resolve; });
    let oldFinished;
    const finished = new Promise(resolve => { oldFinished = resolve; });
    await page.route('**/search?**', async route => {
      if (new URL(route.request().url()).searchParams.get('q') !== 'Browser106') {
        await route.continue(); return;
      }
      try {
        const upstream = await route.fetch();
        observedOld();
        await release;
        await route.fulfill({response:upstream}).catch(()=>{});
      } finally { oldFinished(); }
    });
    await page.getByLabel('Search records',{exact:true}).fill('Browser106');
    await page.getByRole('button',{name:'Search',exact:true}).click();
    await observed;
    await page.getByLabel('Search records',{exact:true}).fill('Separate106');
    await page.locator('input[name=type]:checked').uncheck();
    await page.getByRole('button',{name:'Search',exact:true}).click();
    await remembered(page);
    await page.locator('[data-discovery-results]').getByText(/Separate106 customer/).waitFor();
    releaseOld(); await finished;
    assert.equal(await rows(page).count(),1);
    assert.match(await rows(page).innerText(),/Separate106 customer/);
    assert.equal(new URL(page.url()).searchParams.get('q'),'Separate106');
    await page.unroute('**/search?**');
    console.log('PASS delayed genuine response cannot replace latest query');

    // A real network failure may not display old records as new query results.
    await page.route('**/search?**', route => route.abort('connectionfailed'));
    await page.getByLabel('Search records',{exact:true}).fill('unavailable106');
    await page.getByRole('button',{name:'Search',exact:true}).click();
    await page.waitForFunction(()=>/unavailable|connection|retry/i.test(document.body.innerText));
    assert.equal(await rows(page).filter({visible:true}).count(),0);
    assert.equal(await page.getByLabel('Search records',{exact:true}).inputValue(),'unavailable106');
    await page.unroute('**/search?**');
    await page.goto(shared);
    await remembered(page);
    await page.goto(base+'/preferences');
    assert.equal((await context.request.post(base+'/fixture/revoke')).status(),204);
    const watchUnauthorizedInsertion = () => {
      window.discoveryInsertedRecords = [];
      new MutationObserver(() => {
        for (const row of document.querySelectorAll('[data-discovery-results] [data-record-id]')) {
          window.discoveryInsertedRecords.push(row.dataset.recordId);
        }
      }).observe(document,{subtree:true,childList:true});
    };
    await context.addInitScript(watchUnauthorizedInsertion);
    await page.evaluate(watchUnauthorizedInsertion);
    await page.goBack();
    await page.waitForFunction(()=>!document.body.innerText.includes('Browser106 025'));
    assert.equal(await rows(page).count(),0);
    assert.deepEqual(await page.evaluate(()=>window.discoveryInsertedRecords),[],'Back must not briefly reinsert revoked result rows');
    assert.match(await page.locator('body').innerText(),/authorized|unavailable|Reset/);
    console.log('PASS user isolation, mobile/keyboard, network failure and fresh-authority Back denial');
    await page.goto(base+'/search?q=Separate106');
    await remembered(page);
    assert.equal(await rows(page).count(),1);
    await page.goto(base+'/preferences');
    assert.equal((await context.request.post(base+'/fixture/expire')).status(),204);
    await page.goBack();
    await page.waitForURL(url=>url.pathname==='/login');
    assert.equal(await rows(page).count(),0);
    assert.doesNotMatch(await page.locator('body').innerText(),/Separate106 customer/);
    console.log('PASS revoked real browser session cannot restore cached results');
  } catch (error) {
    await page.screenshot({path:path.join(artifacts,'failure.png'),fullPage:true}).catch(()=>{});
    throw error;
  } finally {
    await context.tracing.stop({path:path.join(artifacts,'trace.zip')});
    await context.close();
  }
})().catch(error=>{console.error(error);process.exit(1);});
