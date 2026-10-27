const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
(async () => {
  const [base, cookie] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext();
    await context.addCookies([{ name: 'session', value: cookie, url: base }]);
    const page = await context.newPage();
    await page.goto(base + '/api/v1/reference');
    await page.getByLabel('Device label').fill('Archived master reference');
    const start = page.waitForResponse(r => new URL(r.url()).pathname === '/api/v1/pairings');
    await page.getByRole('button', { name: 'Start pairing', exact: true }).click();
    const started = await (await start).json();
    const approval = await context.newPage();
    await approval.goto(base + '/pair');
    await approval.getByLabel('Pairing code').fill(started.data.user_code);
    await approval.getByRole('button', { name: 'Review device', exact: true }).click();
    await approval.getByRole('heading', { name: 'Archived master reference', exact: true }).waitFor();
    await approval.getByRole('button', { name: 'Approve device', exact: true }).click();
    await approval.getByText('Device approved.', { exact: false }).waitFor();
    const redeem = page.waitForResponse(r => new URL(r.url()).pathname === '/api/v1/pairings/redeem');
    await page.getByRole('button', { name: 'Redeem approved pairing', exact: true }).click();
    assert.equal((await redeem).status(), 201);
    await page.locator('#sync-start').click();
    await page.waitForFunction(() => document.querySelector('#sync-state').textContent.includes('; complete;'));
    const records = JSON.parse(await page.locator('#sync-records').textContent());
    assert.equal(records.length, 1);
    assert.deepEqual(records[0].masters.assets[0].location, { title: 'Historical place', archived: true });
    assert.equal(records[0].masters.project.location_archived, true);
    assert.doesNotMatch(JSON.stringify(records), /PRIVATE_ARCHIVED_ADDRESS/);
    console.log('Real pairing and reference synchronization retain only archived Location title/badge');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
