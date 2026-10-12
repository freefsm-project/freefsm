const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');

(async () => {
  const [base, cookie, jobID] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE, headless: true, args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = await context.newPage();
    const definitions = [
      ['Short description', 'text', '', 'Created default'],
      ['Observations', 'textarea', '', ''],
      ['Measured pressure', 'number', '', '12.30'],
      ['Inspection date', 'date', '', ''],
      ['Safety reviewed', 'checkbox', '', ''],
      ['Region', 'select', '["North","South"]', ''],
      ['Equipment', 'multiselect', '["Pump","Motor","Fan"]', ''],
    ];
    for (const [name, type, options, fallback] of definitions) {
      assert.equal((await page.goto(base + '/settings/custom-fields/new')).status(), 200);
      await page.locator('[name=name]').fill(name);
      await page.locator('[name=object_type]').selectOption('customer');
      await page.locator('[name=field_type]').selectOption(type);
      if (type === 'number') await page.locator('[name=precision]').fill('2');
      if (options) await page.locator('[name=options]').fill(options);
      if (fallback) await page.locator('[name=default_value]').fill(fallback);
      if (type === 'checkbox') await page.locator('[name=required]').selectOption('true');
      await Promise.all([page.waitForURL(base + '/settings/custom-fields'), page.getByRole('button', {name: 'Save', exact: true}).click()]);
    }
    assert.equal((await page.goto(base + '/customers/new')).status(), 200);
    assert.equal(await page.locator('[name=custom_field_1]').inputValue(), 'Created default');
    assert.equal(await page.locator('[name=custom_field_3]').inputValue(), '12.30');
    await page.locator('[name=display_name]').fill('Browser typed customer');
    await page.locator('[name=custom_field_1]').fill('Explicit browser text');
    await page.locator('[name=custom_field_2]').fill('First line\nSecond line');
    await page.locator('[name=custom_field_3]').fill('4.25');
    await page.locator('[name=custom_field_4]').fill('2024-02-29');
    await page.locator('[name=custom_field_6]').selectOption('South');
    await page.locator('select[name="custom_field_7[]"]').selectOption(['Pump', 'Motor']);
    const rejected = page.waitForResponse(r => r.request().method() === 'POST' && r.url() === base + '/customers');
    await page.getByRole('button', {name: 'Save and continue', exact: true}).click();
    assert.equal((await rejected).status(), 422);
    assert.match(await page.locator('[data-error-summary]').innerText(), /Safety reviewed/);
    assert.equal(await page.locator('[name=display_name]').inputValue(), 'Browser typed customer');
    assert.equal(await page.locator('[name=custom_field_2]').inputValue(), 'First line\nSecond line');
    assert.deepEqual(await page.locator('select[name="custom_field_7[]"]').evaluate(el => [...el.selectedOptions].map(o => o.value)), ['Pump', 'Motor']);
    await page.locator('[name=custom_field_5]').selectOption('false');
    await Promise.all([page.waitForURL(/\/customers\/\d+\/edit$/), page.getByRole('button', {name: 'Save and continue', exact: true}).click()]);
    const editURL = page.url();
    await page.reload();
    assert.equal(await page.locator('[name=custom_field_5]').inputValue(), 'false');
    assert.equal(await page.locator('[name=custom_field_3]').inputValue(), '4.25');
    assert.equal(await page.locator('[name=custom_field_4]').inputValue(), '2024-02-29');
    await page.locator('[name=display_name]').fill('Rejected atomic edit');
    await page.locator('[name=custom_field_3]').fill('4.251');
    const badPrecision = page.waitForResponse(r => r.request().method() === 'POST' && /\/customers\/\d+$/.test(r.url()));
    await page.getByRole('button', {name: 'Save', exact: true}).click();
    assert.equal((await badPrecision).status(), 422);
    assert.match(await page.locator('[data-error-summary]').innerText(), /Measured pressure/);
    await page.goto(editURL);
    assert.equal(await page.locator('[name=display_name]').inputValue(), 'Browser typed customer');
    assert.equal(await page.locator('[name=custom_field_3]').inputValue(), '4.25');
    assert.deepEqual(await page.locator('select[name="custom_field_7[]"]').evaluate(el => [...el.selectedOptions].map(o => o.value)), ['Pump', 'Motor']);
    await page.goto(base + '/settings/custom-fields/new');
    await page.locator('[name=name]').fill('Job safety');
    await page.locator('[name=object_type]').selectOption('job');
    await page.locator('[name=field_type]').selectOption('checkbox');
    await page.locator('[name=required]').selectOption('true');
    await Promise.all([page.waitForURL(base + '/settings/custom-fields'),page.getByRole('button',{name:'Save',exact:true}).click()]);
    await page.goto(base + `/jobs/${jobID}/custom-fields`);
    await page.locator('[name=custom_field_8]').selectOption('false');
    await Promise.all([page.waitForNavigation(),page.getByRole('button',{name:'Save Custom Fields',exact:true}).click()]);
    await page.reload();
    assert.equal(await page.locator('[name=custom_field_8]').inputValue(),'false');
    await page.goto(base + '/settings/custom-fields/new');
    await page.locator('[name=name]').fill('Asset safety');
    await page.locator('[name=object_type]').selectOption('asset');
    await page.locator('[name=field_type]').selectOption('checkbox');
    await page.locator('[name=required]').selectOption('true');
    await Promise.all([page.waitForURL(base + '/settings/custom-fields'),page.getByRole('button',{name:'Save',exact:true}).click()]);
    await page.goto(base + `/jobs/${jobID}/edit`);
    await page.locator('#job-asset-select').locator('..').getByRole('button',{name:'Create',exact:true}).click();
    await page.locator('#job-asset-name').fill('Browser inline asset');
    await page.locator('#job-asset-type-id').selectOption({label:'Browser equipment'});
    const missingAssetField = page.waitForResponse(r => r.request().method()==='POST' && r.url()===base+'/assets/inline');
    await page.locator('#job-asset-create-button').click();
    const missingAssetResponse = await missingAssetField;
    assert.equal(missingAssetResponse.status(),422,await missingAssetResponse.text());
    await page.locator('#job-asset-error').waitFor({state:'visible'});
    assert.match(await page.locator('#job-asset-error').innerText(),/Asset safety/);
    assert.equal(await page.locator('#job-asset-name').inputValue(),'Browser inline asset');
    await page.locator('#job-asset-dialog [name=custom_field_9]').selectOption('false');
    const savedAsset = page.waitForResponse(r => r.request().method()==='POST' && r.url()===base+'/assets/inline');
    await page.locator('#job-asset-create-button').click();
    assert.equal((await savedAsset).status(),200);
    await page.locator('#job-asset-dialog').waitFor({state:'hidden'});
    const assetID = await page.locator('#job-asset-select').inputValue();
    assert.notEqual(assetID,'0');
    await page.goto(base + `/assets/${assetID}/edit`);
    assert.equal(await page.locator('[name=custom_field_9]').inputValue(),'false');
    await page.goto(base + '/settings/custom-fields/new');
    await page.locator('[name=name]').fill('Project reference');
    await page.locator('[name=object_type]').selectOption('project');
    await page.locator('[name=field_type]').selectOption('text');
    await page.locator('[name=required]').selectOption('true');
    await Promise.all([page.waitForURL(base + '/settings/custom-fields'),page.getByRole('button',{name:'Save',exact:true}).click()]);
    await page.goto(base + `/jobs/${jobID}/edit`);
    await page.locator('#job-project-select').locator('..').getByRole('button',{name:'Create',exact:true}).click();
    await page.locator('#job-project-name').fill('Browser inline project');
    await page.locator('#job-project-description').fill('Retained project description');
    await page.locator('#job-project-completion-percentage').fill('37');
    const missingProjectField = page.waitForResponse(r => r.request().method()==='POST' && r.url()===base+'/projects/inline');
    await page.locator('#job-project-create-button').click();
    assert.equal((await missingProjectField).status(),422);
    await page.locator('#job-project-error').waitFor({state:'visible'});
    assert.match(await page.locator('#job-project-error').innerText(),/Project reference/);
    assert.equal(await page.locator('#job-project-description').inputValue(),'Retained project description');
    assert.equal(await page.locator('#job-project-completion-percentage').inputValue(),'37');
    await page.locator('#job-project-dialog [name=custom_field_10]').fill('Project entered value');
    const savedProject = page.waitForResponse(r => r.request().method()==='POST' && r.url()===base+'/projects/inline');
    await page.locator('#job-project-create-button').click();
    assert.equal((await savedProject).status(),200);
    await page.locator('#job-project-dialog').waitFor({state:'hidden'});
    const projectID = await page.locator('#job-project-select').inputValue();
    assert.notEqual(projectID,'0');
    await page.goto(base + `/projects/${projectID}/edit`);
    assert.equal(await page.locator('[name=custom_field_10]').inputValue(),'Project entered value');
    assert.equal(await page.locator('[name=description]').inputValue(),'Retained project description');
    assert.equal(await page.locator('[name=completion_percentage]').inputValue(),'37');
    async function defineInlineField(parent, name, type, fallback = '') {
      await page.goto(base + '/settings/custom-fields/new');
      await page.locator('[name=name]').fill(name);
      await page.locator('[name=object_type]').selectOption(parent);
      await page.locator('[name=field_type]').selectOption(type);
      if (type === 'checkbox') await page.locator('[name=required]').selectOption('true');
      if (type === 'select' || type === 'multiselect') await page.locator('[name=options]').fill('["Default","First","Second"]');
      if (fallback) await page.locator('[name=default_value]').fill(fallback);
      await Promise.all([page.waitForURL(base + '/settings/custom-fields'), page.getByRole('button', {name:'Save',exact:true}).click()]);
      const edit = await page.getByRole('row').filter({has:page.getByText(name,{exact:true})}).getByRole('button',{name:'Edit',exact:true}).getAttribute('href');
      return /\/custom-fields\/(\d+)\/edit$/.exec(edit)[1];
    }
    for (const parent of ['asset', 'project']) {
      const checkbox = parent === 'asset' ? '9' : await defineInlineField(parent, 'Project reuse safety', 'checkbox');
      const text = await defineInlineField(parent, `Reuse ${parent} text`, 'text', `Initial ${parent} default`);
      const single = await defineInlineField(parent, `Reuse ${parent} choice`, 'select', 'Default');
      const multi = await defineInlineField(parent, `Reuse ${parent} choices`, 'multiselect', '["Default"]');
      const dialog = page.locator(`#job-${parent}-dialog`);
      const field = id => dialog.locator(`[name="custom_field_${id}"]`);
      const selections = () => dialog.locator(`select[name="custom_field_${multi}[]"]`).evaluate(el => [...el.selectedOptions].map(o => o.value));
      const open = () => page.locator(`#job-${parent}-select`).locator('..').getByRole('button',{name:'Create',exact:true}).click();
      const submit = async status => {
        const response = page.waitForResponse(r => r.request().method()==='POST' && r.url()===base+`/${parent}s/inline`);
        await page.locator(`#job-${parent}-create-button`).click();
        const saved = await response;
        assert.equal(saved.status(),status,await saved.text());
        if (status===200) await dialog.waitFor({state:'hidden'});
        else await page.locator(`#job-${parent}-error`).waitFor({state:'visible'});
      };
      const nameRecord = async ordinal => {
        await page.locator(`#job-${parent}-name`).fill(`Reuse ${ordinal} ${parent}`);
        if (parent==='asset') await page.locator('#job-asset-type-id').selectOption({label:'Browser equipment'});
        else await field('10').fill(`${ordinal} project reference`);
      };
      await page.goto(base + `/jobs/${jobID}/edit`);
      await open();
      assert.equal(await field(text).inputValue(),`Initial ${parent} default`);
      await nameRecord('first');
      await field(text).fill(`First ${parent} answer`);
      await field(single).selectOption('First');
      await dialog.locator(`select[name="custom_field_${multi}[]"]`).selectOption(['First','Second']);
      await submit(422);
      assert.equal(await field(text).inputValue(),`First ${parent} answer`);
      assert.equal(await field(single).inputValue(),'First');
      assert.deepEqual(await selections(),['First','Second']);
      await field(checkbox).selectOption('true');
      await submit(200);
      const firstID = await page.locator(`#job-${parent}-select`).inputValue();

      // Change a default in another tab: the second creation must start from
      // current definitions, not the original page or the first error response.
      const control = await context.newPage();
      await control.goto(base + `/settings/custom-fields/${text}/edit`);
      await control.locator('[name=default_value]').fill(`Current ${parent} default`);
      await Promise.all([control.waitForURL(base + '/settings/custom-fields'),control.getByRole('button',{name:'Save',exact:true}).click()]);
      await control.close();

      await open(); // Same Job page and same dialog; no reload between creations.
      assert.equal(await field(checkbox).inputValue(),'',`${parent}: previous Yes must not authorize a new record`);
      assert.equal(await field(text).inputValue(),`Current ${parent} default`);
      assert.equal(await field(single).inputValue(),'Default');
      assert.deepEqual(await selections(),['Default']);
      if (parent==='project') assert.equal(await field('10').inputValue(),'');
      await nameRecord('second');
      await submit(422);
      assert.equal(await field(text).inputValue(),`Current ${parent} default`);
      assert.equal(await field(checkbox).inputValue(),'');
      assert.deepEqual(await selections(),['Default']);
      await field(checkbox).selectOption('false');
      await submit(200);
      const secondID = await page.locator(`#job-${parent}-select`).inputValue();
      assert.notEqual(firstID,secondID);
      for (const [id,answer,checked,choice,choices] of [
        [firstID,`First ${parent} answer`,'true','First',['First','Second']],
        [secondID,`Current ${parent} default`,'false','Default',['Default']],
      ]) {
        await page.goto(base + `/${parent}s/${id}/edit`);
        assert.equal(await page.locator(`[name="custom_field_${text}"]`).inputValue(),answer);
        assert.equal(await page.locator(`[name="custom_field_${checkbox}"]`).inputValue(),checked);
        assert.equal(await page.locator(`[name="custom_field_${single}"]`).inputValue(),choice);
        assert.deepEqual(await page.locator(`select[name="custom_field_${multi}[]"]`).evaluate(el => [...el.selectedOptions].map(o => o.value)),choices);
      }
    }
    // A form opened before retirement is pending intent, never permission to
    // overwrite retained history. Reactivation does not replay that submission.
    await page.goto(editURL);
    await page.locator('[name=custom_field_1]').fill('Pending retired edit');
    const control = await context.newPage();
    await control.goto(base + '/settings/custom-fields/1/edit');
    await control.locator('[name=retired]').selectOption('true');
    await Promise.all([control.waitForURL(base + '/settings/custom-fields'),control.getByRole('button',{name:'Save',exact:true}).click()]);
    const retiredResponse = page.waitForResponse(r => r.request().method()==='POST' && /\/customers\/\d+$/.test(r.url()));
    await page.getByRole('button',{name:'Save',exact:true}).click();
    assert.equal((await retiredResponse).status(),422);
    await page.goto(editURL);
    assert.equal(await page.locator('[name=custom_field_1]').count(),0);
    await page.goto(editURL.replace(/\/edit$/,''));
    assert.match(await page.locator('body').innerText(),/Explicit browser text/);
    await control.goto(base + '/settings/custom-fields/1/edit');
    await control.locator('[name=retired]').selectOption('false');
    await Promise.all([control.waitForURL(base + '/settings/custom-fields'),control.getByRole('button',{name:'Save',exact:true}).click()]);
    await page.goto(editURL);
    assert.equal(await page.locator('[name=custom_field_1]').inputValue(),'Explicit browser text');
    await control.close();
    console.log('Persisted production browser: typed fields, atomic correction, inline creation, retirement rejects pending edits, history survives and reactivation never replays.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
