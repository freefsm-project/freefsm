const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');
(async () => {
  const [base, cookie, id] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = await context.newPage();
    await page.goto(base + '/items');
    const list = page.locator('#item-list-content');
    assert.equal(await list.getByRole('link', { name: 'Browser archived catalog', exact: true }).count(), 0);
    await page.getByRole('link', { name: 'Archived Items', exact: true }).click();
    await list.getByRole('link', { name: 'Browser archived catalog', exact: true }).waitFor();
    const search = page.locator('.focused-list-search input[name="search"]');
    await search.fill('Browser archived');
    const submitted = page.waitForResponse(response => {
      const url = new URL(response.url());
      return response.request().method() === 'GET' && url.pathname === '/items' && url.searchParams.get('search') === 'Browser archived';
    });
    await search.press('Enter');
    const result = await submitted;
    assert.equal(result.status(),200);
    assert.deepEqual(new URL(result.url()).searchParams.getAll('archived'),['1'],'submitting search sends archived exactly once');
    await list.getByRole('link', { name: 'Browser archived catalog', exact: true }).waitFor();
    await page.locator('[data-list-save-status]').getByText('Filters remembered', {exact:true}).waitFor();
    assert.equal(await search.locator('xpath=ancestor::form').locator('input[name="archived"][value="1"]').count(), 1);
    assert.equal(await page.locator(`form[action="/items/${id}/delete"]`).count(), 0);
    await list.getByRole('link', { name: 'Browser archived catalog', exact: true }).click();
    await page.getByText(/Archived on/).waitFor();
    assert.match(await page.locator('body').innerText(), /Archived on/);
    await Promise.all([page.waitForNavigation(), page.getByRole('button', { name: 'Restore', exact: true }).click()]);
    await page.goto(base + `/items/${id}`);
    assert.doesNotMatch(await page.locator('body').innerText(), /Archived on/);
    console.log('Archived Item discovery, scoped search, read-only list and persisted Restore passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
