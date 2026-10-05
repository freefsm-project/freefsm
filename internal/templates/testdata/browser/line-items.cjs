const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');

// Drain reactive DOM work and nextTick focus work without fixed sleeps.
const settle = page => page.evaluate(() => new Promise(resolve =>
  window.Alpine.nextTick(() => requestAnimationFrame(resolve))));

function payload(rows) {
  return rows.filter(row => row['item.title'] || Number(row['item.item_id']) > 0).map(row => ({
    item_id: Number(row['item.item_id']), title: row['item.title'], description: row['item.description'],
    quantity: Number(row['item.quantity']), unit_price: Number(row['item.unit_price']),
    discount: Number(row['item.discount']), surcharge: Number(row['item.surcharge']),
    taxable: row['item.taxable'], tax_rate: '',
  }));
}

async function main() {
  const browser = await chromium.launch({
    executablePath: process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium-browser',
    headless: true,
  });
  let failures = 0;
  try {
    for (const form of ['invoice/create', 'invoice/edit', 'estimate/create', 'estimate/edit']) {
      for (const field of ['title', 'quantity', 'unit_price', 'discount', 'surcharge', 'description', 'controls', 'composition']) {
        const page = await browser.newPage();
        const errors = [];
        const posts = [];
        page.on('pageerror', error => errors.push(error.message));
        page.on('request', request => {
          if (request.method() === 'POST') posts.push(request.url());
        });
        try {
          // Only the local fixture server is reachable; all JS/CSS are production assets.
          await page.route('**/*', route => new URL(route.request().url()).origin === process.argv[2]
            ? route.continue() : route.abort());
          await page.goto(`${process.argv[2]}/${form}`);
          const rows = page.locator('[data-line-item-editor] tbody > tr');
          await rows.nth(1).waitFor();
          assert.equal(await rows.count(), 2);
          const snapshot = () => rows.evaluateAll(elements => elements.map(row =>
            Object.fromEntries([...row.querySelectorAll('[x-model], [x-model\\.number]')].map(input => [
              input.getAttribute('x-model') || input.getAttribute('x-model.number'),
              input.type === 'checkbox' ? input.checked : input.value,
            ]))));
          const before = await snapshot();
          assert.deepEqual(before.map(row => row['item.title']), ['First distinct line', 'Second distinct line']);
          assert.equal(await page.locator('form.compact-form').evaluate(form => form.checkValidity()), true, 'fixture must allow submission so validation cannot mask accidental Save');
          const input = rows.nth(1).locator(`[x-model="item.${['controls', 'composition'].includes(field) ? 'title' : field}"]`);
          if (field === 'controls') {
            await rows.nth(0).getByRole('button', { name: 'Remove line item' }).click();
            await settle(page);
            assert.deepEqual(await snapshot(), [before[1]], 'intentional Remove must remove only the selected row');
            await page.getByRole('button', { name: 'Add Line', exact: true }).click();
            await settle(page);
            assert.equal(await rows.count(), 2, 'Add Line appends exactly one row');
            assert.deepEqual((await snapshot())[0], before[1]);
            assert.equal(await input.inputValue(), '');
            await input.fill('Added by button');
            await page.getByRole('button', { name: 'Create Item', exact: true }).first().click();
            assert.equal(await page.locator('.item-create-dialog').evaluate(dialog => dialog.open), true);
            await page.getByRole('button', { name: 'Close', exact: true }).click();
          } else if (field === 'composition') {
            await input.focus();
            // Native OS IME is unavailable headlessly; exercise both browser event guards.
            for (const properties of [{ isComposing: true }, { keyCode: 229 }]) {
              await input.dispatchEvent('keydown', { key: 'Enter', code: 'Enter', bubbles: true, cancelable: true, ...properties });
              await settle(page);
              assert.deepEqual(await snapshot(), before, 'IME confirmation must not add/remove a row');
              assert.equal(await input.evaluate(element => element === document.activeElement), true);
            }
          } else {
            await input.focus();
            if (field === 'description') await input.press('End');
            // Repeated keydown is a real held-key repeat, including after focus moves.
            await page.keyboard.down('Enter');
            await settle(page);
            if (field === 'title') await page.keyboard.down('Enter');
            await page.keyboard.up('Enter');
          }
          await settle(page);
          const after = await snapshot();
          console.log(`${form}/${field}: rows 2 -> ${after.length}; titles ${JSON.stringify(after.map(row => row['item.title']))}`);
          assert.deepEqual(errors, [], 'browser JavaScript errors');
          assert.deepEqual(posts, [], 'Enter must not submit the document');
          assert.equal(page.url(), `${process.argv[2]}/${form}`, 'Enter must not navigate');
          if (field === 'description') {
            before[1]['item.description'] += '\n';
            assert.deepEqual(after, before, 'description Enter must only insert a newline');
            assert.equal(await input.evaluate(element => element === document.activeElement), true);
          } else if (!['controls', 'composition'].includes(field)) {
            assert.equal(after.length, 3, 'Enter must preserve both rows and append one blank line');
            assert.deepEqual(after.slice(0, 2), before, 'existing rows must retain all values and order');
            assert.equal(after[2]['item.title'], '', 'new bottom line must have a blank title');
            assert.equal(after[2]['item.description'], '', 'new bottom line must have a blank description');
            assert.equal(await rows.nth(2).locator('[x-model="item.title"]').evaluate(element => element === document.activeElement), true, 'new bottom title must receive focus');
          }
          // Blank appended lines are omitted; filled Add Line rows are serialized.
          const submitted = page.waitForRequest(request => request.method() === 'POST');
          await page.getByRole('button', { name: 'Save', exact: true }).click();
          const request = await submitted;
          const expectedAction = `/${form.startsWith('invoice') ? 'invoices' : 'estimates'}${form.endsWith('edit') ? '/7' : ''}`;
          assert.equal(new URL(request.url()).pathname, expectedAction);
          const body = new URLSearchParams(request.postData());
          assert.equal(body.get('customer_id'), '1');
          assert.equal(body.get('title'), 'Enter regression');
          assert.deepEqual(JSON.parse(body.get('line_items')), payload(after), 'explicit Save must serialize the preserved document lines');
          assert.equal(posts.length, 1, 'only explicit Save may submit');
          assert.deepEqual(errors, []);
          console.log(`PASS ${form}/${field}`);
        } catch (error) {
          failures++;
          console.error(`FAIL ${form}/${field}: ${error.message}`);
        } finally {
          await page.close();
        }
      }
    }
  } finally {
    await browser.close();
  }
  if (failures) process.exitCode = 1;
}

main().catch(error => { console.error(error); process.exitCode = 1; });
