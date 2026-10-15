const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');
(async () => {
  const [base, cookie, customer] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = await context.newPage();
    for (const kind of ['estimate', 'invoice']) {
      await page.goto(base + '/settings/custom-fields/new');
      await page.locator('[name=name]').fill(`Commercial ${kind} reading`);
      await page.locator('[name=object_type]').selectOption(kind);
      await page.locator('[name=field_type]').selectOption('text');
      await page.locator('[name=required]').selectOption('true');
      await page.locator('[name=default_value]').fill('Initial field default');
      await Promise.all([page.waitForURL(base + '/settings/custom-fields'), page.getByRole('button', { name: 'Save', exact: true }).click()]);
    }
    for (const kind of ['estimate', 'invoice']) {
      assert.equal((await page.goto(`${base}/${kind}s/new?customer_id=${customer}`)).status(), 200);
      await page.locator('[name=title]').fill(`Browser ${kind}`);
      await page.locator('[name=notes]').fill('Original browser note');
      const field = page.locator('input[name^="custom_field_"]');
      assert.equal(await field.inputValue(), 'Initial field default');
      await field.fill('Explicit browser field');
      await page.locator('[aria-label="Line item title"]').first().fill('Browser captured work');
      await page.locator('[aria-label="Quantity"]').first().fill('1.5');
      await page.locator('[aria-label="Unit price"]').first().fill('12.50');
      if (kind === 'invoice') { await page.locator('[name=invoice_date]').fill('2026-10-13'); await page.locator('[name=due_date]').fill('2026-11-13'); }
      await Promise.all([page.waitForURL(new RegExp(`/${kind}s(?:/[0-9]+)?(?:\\?|$)`)), page.getByRole('button', { name: 'Save', exact: true }).click()]);
      if (page.url().split('?')[0].endsWith(`/${kind}s`)) {
        await Promise.all([page.waitForURL(new RegExp(`/${kind}s/[0-9]+(?:\\?|$)`)), page.getByRole('link', { name: `Browser ${kind}`, exact: true }).first().click()]);
      }
      const detail = page.url().split('?')[0];
      assert.match(detail, new RegExp(`/${kind}s/[0-9]+$`));
      await page.goto(detail + '/edit');
      const other = await context.newPage();
      await other.goto(detail + '/edit');
      await page.locator('[name=notes]').fill(kind === 'invoice' ? '' : 'First browser edit');
      await page.locator('input[name^="custom_field_"]').fill('Revised browser field');
      if (kind === 'invoice') await page.locator('[name=due_date]').fill('');
      await Promise.all([page.waitForURL(new RegExp(`/${kind}s/[0-9]+\\?`)), page.getByRole('button', { name: 'Save', exact: true }).click()]);
      await other.locator('[name=terms]').fill('Disjoint stale browser intent');
      const conflict = other.waitForResponse(r => r.request().method() === 'POST' && r.url() === detail);
      await other.getByRole('button', { name: 'Save', exact: true }).click();
      assert.equal((await conflict).status(), 409);
      await other.close();
      await page.goto(detail + '/edit');
      assert.equal(await page.locator('[name=notes]').inputValue(), kind === 'invoice' ? '' : 'First browser edit');
      assert.notEqual(await page.locator('[name=terms]').inputValue(), 'Disjoint stale browser intent');
      assert.equal(await page.locator('input[name^="custom_field_"]').inputValue(), 'Revised browser field');
      assert.equal(await page.locator('[aria-label="Line item title"]').first().inputValue(), 'Browser captured work');
      assert.equal(Number(await page.locator('[aria-label="Quantity"]').first().inputValue()), 1.5);
      assert.equal(Number(await page.locator('[aria-label="Unit price"]').first().inputValue()), 12.5);
      if (kind === 'invoice') assert.equal(await page.locator('[name=due_date]').inputValue(), '');
      await page.goto(detail);
      if (kind === 'invoice') {
        assert.equal(await page.locator('select[name=status_id]').count(), 0, 'Draft must offer Finalize, not manual exit');
        page.once('dialog', dialog => dialog.accept());
        await Promise.all([page.waitForURL(/\?flash=/), page.getByRole('button', { name: 'Finalize Invoice', exact: true }).click()]);
        for (const label of ['Void', 'Sent', 'Void', 'Invoiced', 'Draft']) {
          const labels = await page.locator('select[name=status_id] option').allTextContents();
          assert.ok(!labels.includes('Paid') && !labels.includes('Partially Paid'), 'derived statuses cannot be selected');
          await page.locator('select[name=status_id]').selectOption({ label });
          await Promise.all([page.waitForResponse(r => r.request().method() === 'POST' && r.url().endsWith('/status')), page.getByRole('button', { name: 'Save status', exact: true }).click()]);
          await page.goto(detail);
        }
        assert.equal(await page.getByRole('button', { name: 'Finalize Invoice', exact: true }).count(), 1);
      } else {
        await page.locator('select[name=status_id]').selectOption({ label: 'Accepted' });
        await Promise.all([page.waitForResponse(r => r.request().method() === 'POST' && r.url().endsWith('/status')), page.getByRole('button', { name: 'Save status', exact: true }).click()]);
        await page.goto(detail + '/edit');
        assert.equal(await page.locator('[name=title]').inputValue(), 'Browser estimate', 'Accepted Estimate remains editable');
      }
      if (process.env.COMMERCIAL_EVIDENCE_DIR) await page.screenshot({ path: `${process.env.COMMERCIAL_EVIDENCE_DIR}/${kind}-phone.png`, fullPage: true });
    }
    console.log('PASS persisted phone commercial create/edit, typed field defaults and edits, editable line snapshots, explicit empty values, stale disjoint conflict');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });
