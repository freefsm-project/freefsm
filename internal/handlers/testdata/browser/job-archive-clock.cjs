const {chromium} = require('playwright-core');
const assert = require('node:assert/strict');
(async () => {
  const [base, adminToken, workerToken, job, evidence] = process.argv.slice(2);
  const browser = await chromium.launch({executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox']});
  try {
    const admin = await browser.newContext();
    const worker = await browser.newContext({viewport: {width: 320, height: 740}});
    await admin.addCookies([{name: 'session', value: adminToken, url: base}]);
    await worker.addCookies([{name: 'session', value: workerToken, url: base}]);
    const office = await admin.newPage();
    const field = await worker.newPage();
    await field.goto(`${base}/jobs/${job}`);
    await field.getByRole('button', {name: 'Clock In to Job', exact: true}).click();
    await field.getByText('You are clocked in to this job', {exact: true}).waitFor();
    const entry = await field.locator(`form[action="/jobs/${job}/clock-out"] input[name="entry_id"]`).inputValue();
    await office.goto(`${base}/jobs/${job}`);
    office.on('dialog', dialog => dialog.accept());
    await office.locator(`form[action="/jobs/${job}/delete"] button`).click();
    await office.waitForURL('**/jobs?flash=Job+archived');
    await office.goto(`${base}/jobs/${job}`);
    await office.getByText('Archived on', {exact: false}).waitFor();
    const denied = await field.goto(`${base}/jobs/${job}`);
    assert.equal(denied.status(), 403);
    await field.goto(base);
    const out = field.locator('form[action="/time-entries/clock-out"]');
    assert.equal(await out.locator('input[name="entry_id"]').inputValue(), entry);
    assert.equal(await field.locator(`a[href="/jobs/${job}"]`).count(), 0);
    assert.doesNotMatch(await field.locator('body').innerText(), /PRIVATE/);
    await field.evaluate(() => {
      navigator.geolocation.getCurrentPosition = ok => ok({coords: {latitude: 51.5, longitude: -0.12, accuracy: 4}, timestamp: Date.now() - 1000});
    });
    await out.getByRole('button', {name: 'Clock with location', exact: true}).click();
    await field.getByRole('button', {name: 'Clock In', exact: true}).waitFor();
    await field.reload();
    await field.getByRole('button', {name: 'Clock In', exact: true}).waitFor();
    assert.equal(await field.locator(`a[href="/jobs/${job}"]`).count(), 0);
    assert.ok(await field.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth));
    await field.screenshot({path: `${evidence}/archived-own-clock.png`, fullPage: true});
    console.log('Archive while actively clocked -> no Job context -> retained exact own entry out with GPS -> reload; phone reflow passed');
  } finally {
    await browser.close();
  }
})().catch(error => {console.error(error); process.exit(1);});
