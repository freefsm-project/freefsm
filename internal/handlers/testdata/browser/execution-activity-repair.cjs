'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {randomUUID} = require('node:crypto');
const {chromium} = require('playwright-core');

(async () => {
  const [base, cookie, job, token] = process.argv.slice(2);
  const browser = await chromium.launch({executablePath: process.env.BROWSER_EXECUTABLE, args:['--no-sandbox']});
  try {
    const context = await browser.newContext({viewport:{width:320,height:900}});
    await context.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
    const page = await context.newPage();
    for (const text of ['Browser original observation','Browser revised observation']) {
      await page.goto(`${base}/jobs/${job}/field-notes`);
      await page.locator('[name=field_notes]').fill(text);
      await page.getByRole('button',{name:'Save Field Notes',exact:true}).click();
      await page.waitForURL(/flash=/);
      await page.reload();
      assert.equal(await page.locator('[name=field_notes]').inputValue(),text);
    }
    await page.goto(`${base}/jobs/${job}/subtasks`);
    const change = async action => {
      const [response] = await Promise.all([page.waitForResponse(r=>r.request().method()==='POST'),page.waitForEvent('load'),action()]);
      assert.equal(response.status(),200);
    };
    await page.getByLabel('New Subtask title').fill('Browser original checklist');
    await change(()=>page.getByRole('button',{name:'Add Subtask',exact:true}).click());
    const id = await page.locator('[data-subtask-id]').first().getAttribute('data-subtask-id');
    let row = page.locator(`[data-subtask-id="${id}"]`);
    await row.getByLabel('Subtask title',{exact:true}).fill('Browser revised checklist');
    await change(()=>row.getByRole('button',{name:'Save title'}).click());
    await page.getByLabel('New Subtask title').fill('Browser second checklist');
    await change(()=>page.getByRole('button',{name:'Add Subtask',exact:true}).click());
    await change(()=>page.getByRole('button',{name:'Move up'}).click());
    await change(()=>row.getByRole('button',{name:'Complete',exact:true}).click());
    await change(()=>row.getByRole('button',{name:'Reopen',exact:true}).click());
    page.once('dialog',dialog=>dialog.accept());
    await change(()=>row.getByRole('button',{name:'Remove Subtask'}).click());
    assert.equal(await row.count(),0);
    const call = async (suffix, method='GET', body) => {
      const response = await context.request.fetch(`${base}/api/v1/jobs/${job}/${suffix}`, {method,headers:{Authorization:'Bearer '+token},data:body});
      assert.equal(response.status(),200,await response.text());
      return (await response.json()).data;
    };
    const choices = await call('status-intents');
    const intent = {...choices.find(v=>v.status.category==='job:pending').intent,operation_id:randomUUID(),occurred_at:'2020-02-03T04:05:06Z'};
    await call('status','POST',intent);
    await call('status','POST',intent);
    const records = (await call('activity')).records;
    assert.equal(records.length,10);
    assert.equal(records.find(v=>v.action==='status_transitioned').occurred,'2020-02-03T04:05:06Z');
    const wanted = {field_notes_updated:2,subtask_added:2,subtask_renamed:1,subtasks_reordered:1,subtask_completed:1,subtask_uncompleted:1,subtask_removed:1,status_transitioned:1};
    for (const [action,count] of Object.entries(wanted)) assert.equal(records.filter(v=>v.action===action).length,count);
    for (const [label,url] of [['record',`/jobs/${job}/activity`],['cross-record',`/activity?object_type=job&object_id=${job}`]]) {
      await page.goto(base+url);
      await page.reload();
      const text = await page.locator('body').innerText();
      for (const value of ['Browser original observation','Browser revised observation','Browser original checklist','Browser revised checklist','2020-02-03 04:05:06 UTC','changed Field Notes on','completed a subtask on','uncompleted a subtask on','subtask_added','subtask_renamed','subtasks_reordered','subtask_removed','status_transitioned']) assert(text.includes(value),`${label} missing ${value}`);
      if (process.env.EXECUTION_ACTIVITY_EVIDENCE_DIR) {
        fs.mkdirSync(process.env.EXECUTION_ACTIVITY_EVIDENCE_DIR,{recursive:true});
        await page.screenshot({path:path.join(process.env.EXECUTION_ACTIVITY_EVIDENCE_DIR,label+'.png'),fullPage:true});
        fs.writeFileSync(path.join(process.env.EXECUTION_ACTIVITY_EVIDENCE_DIR,label+'.html'),await page.content());
      }
    }
    console.log(JSON.stringify({result:'passed',persistedEvents:10,width:320,actions:wanted,occurrence:'2020-02-03T04:05:06Z'}));
  } finally { await browser.close(); }
})().catch(error=>{console.error(error);process.exitCode=1;});
