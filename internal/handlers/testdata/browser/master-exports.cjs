const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');

(async () => {
  const [base, cookie, archivedItemID] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium', headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({ viewport: { width: 390, height: 844 }, acceptDownloads: true });
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = await context.newPage();
    for (const route of ['/contacts/export', '/locations/export', '/assets/export', '/projects/export', '/service-entries/export', '/items/export', '/pricebook/export', '/items/cost/export']) {
      // Discover the action through the persisted export hub, rather than
      // treating a guessed review URL as browser navigation coverage.
      await page.goto(base + '/exports');
      await Promise.all([page.waitForURL(base + route), page.locator(`a[href="${route}"]`).click()]);
      await page.getByRole('button', { name: 'Confirm export', exact: true }).waitFor();
      assert.match(await page.locator('body').innerText(), /All matching rows/);
      await Promise.all([page.waitForURL(/\/exports\/\d+$/), page.getByRole('button', { name: 'Confirm export', exact: true }).click()]);
      const result = new URL(page.url()).pathname;
      await page.goto(base + '/exports');
      await page.getByRole('heading', { name: 'My CSV exports', exact: true }).waitFor();
      await Promise.all([page.waitForURL(base + result), page.locator(`a[href="${result}"]`).click()]);
      await page.getByRole('heading', { name: 'CSV export', exact: true }).waitFor();
      for (let i = 0; i < 40 && !(await page.getByRole('link', { name: 'Download CSV', exact: true }).count()); i++) {
        await page.waitForTimeout(250);
        await page.reload();
      }
      const populated = ['/contacts/export', '/items/export', '/pricebook/export', '/items/cost/export'].includes(route);
      assert.match(await page.locator('body').innerText(), populated ? /Completed: 2 rows/ : /No matching rows/);
      const [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('link', { name: 'Download CSV', exact: true }).click()]);
      assert.equal(await download.failure(), null);
      const stream = await download.createReadStream();
      let csv = ''; for await (const chunk of stream) csv += chunk;
      assert.ok(csv.trim().length > 0, `${route}: header-only artifact must have columns`);
      const lines = csv.trim().split(/\r?\n/);
      assert.equal(lines.length, populated ? 3 : 1);
      if (populated) {
        assert.notEqual(lines[1].split(',')[0], lines[2].split(',')[0], 'duplicate labels retain distinct stable identities');
        assert.match(csv, route === '/contacts/export' ? /Browser duplicate contact/ : /Browser duplicate item/);
        if (route === '/contacts/export') assert.match(csv, /Browser export parent/);
        if (route !== '/items/cost/export') assert.doesNotMatch(csv, /123\.45|unit_cost/);
        if (route === '/items/export') assert.doesNotMatch(csv, /876\.54|BROWSER-SKU|unit_price/);
        if (route === '/pricebook/export') { assert.match(csv, /876\.54/); assert.match(csv, /BROWSER-SKU-A/); assert.match(csv, /BROWSER-SKU-B/); }
        if (route === '/items/cost/export') assert.match(csv, /123\.45/);
      }
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth));
      console.log(`${route}: persisted hub navigation, review, request, recovery, Ready ${populated ? 'duplicate identities/facet isolation' : 'empty result'} and authenticated download`);
    }
    // Enter through the actual filtered collection. The name does not match
    // the query: membership depends on authorized SKU search, while this base
    // CSV must still omit every Pricebook and Cost field.
    await page.goto(base + '/items?archived=1&search=BROWSER-RESERVED');
    await page.getByRole('link', { name: 'Export Item base CSV', exact: true }).waitFor();
    assert.match(await page.locator('body').innerText(), /Selected archived browser item/);
    await Promise.all([
      page.waitForURL(url => url.pathname === '/items/export'),
      page.getByRole('link', { name: 'Export Item base CSV', exact: true }).click(),
    ]);
    await page.getByRole('button', { name: 'Confirm export', exact: true }).waitFor();
    for (const [key, value] of Object.entries({ search: 'BROWSER-RESERVED', archived: '1', archived_only: '1', search_sku: '1' })) {
      assert.equal(new URL(page.url()).searchParams.get(key), value, `collection link preserves ${key}`);
      assert.equal(await page.locator(`form input[name="${key}"]`).inputValue(), value, `review submission preserves ${key}`);
    }
    assert.equal(await page.locator('form input[name="sort"]').inputValue(), 'name');
    assert.match(await page.locator('body').innerText(), /Archived records only/);
    assert.match(await page.locator('body').innerText(), /Search includes SKU matches/);
    await Promise.all([page.waitForURL(/\/exports\/\d+$/), page.getByRole('button', { name: 'Confirm export', exact: true }).click()]);
    const filteredResult = new URL(page.url()).pathname;
    await page.goto(base + '/exports');
    await Promise.all([page.waitForURL(base + filteredResult), page.locator(`a[href="${filteredResult}"]`).click()]);
    await page.getByRole('heading', { name: 'CSV export', exact: true }).waitFor();
    for (let i = 0; i < 40 && !(await page.getByRole('link', { name: 'Download CSV', exact: true }).count()); i++) {
      await page.waitForTimeout(250);
      await page.reload();
    }
    assert.match(await page.locator('body').innerText(), /Completed: 1 rows/);
    const [filteredDownload] = await Promise.all([page.waitForEvent('download'), page.getByRole('link', { name: 'Download CSV', exact: true }).click()]);
    assert.equal(await filteredDownload.failure(), null);
    const filteredStream = await filteredDownload.createReadStream();
    let filteredCSV = ''; for await (const chunk of filteredStream) filteredCSV += chunk;
    const filteredLines = filteredCSV.trim().split(/\r?\n/);
    assert.equal(filteredLines.length, 2);
    assert.equal(filteredLines[1].split(',')[0], archivedItemID);
    assert.match(filteredCSV, /Selected archived browser item/);
    assert.doesNotMatch(filteredCSV, /Excluded archived|Browser duplicate|BROWSER-RESERVED|876\.54|123\.45|unit_price|unit_cost/);
    console.log('Filtered Item collection: archived-only + SKU membership preserved through review, durable navigation and exact-identity base CSV download without sensitive facets.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });
