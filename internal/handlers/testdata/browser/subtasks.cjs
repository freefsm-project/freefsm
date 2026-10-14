'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const {chromium}=require('playwright-core');
const [base,cookie,jobID]=process.argv.slice(2);
(async()=>{
  const browser=await chromium.launch({headless:true,executablePath:process.env.BROWSER_EXECUTABLE,args:['--no-sandbox']});
  try {
    const context=await browser.newContext({viewport:{width:320,height:740},isMobile:true,hasTouch:true});
    await context.addCookies([{name:'session',value:cookie,url:base}]);
    const page=await context.newPage();
    await page.goto(`${base}/jobs/${jobID}/subtasks`);
    page.on('pageerror',error=>console.error('page error:',error.message));
    const reloadAfter=async(action)=>{
      const [response]=await Promise.all([page.waitForResponse(r=>r.request().method()==='POST'),page.waitForEvent('load'),action()]);
      assert.equal(new URL(response.url()).pathname,`/jobs/${jobID}/subtasks`,'form action must not be shadowed by its named submit button');
      assert.equal(response.status(),200,'checklist command must be confirmed before reload');
    };
    await page.getByLabel('New Subtask title').fill('Browser pump');
    await reloadAfter(()=>page.getByRole('button',{name:'Add Subtask',exact:true}).click());
    let row=page.locator('[data-subtask-id]').first();const first=await row.getAttribute('data-subtask-id');assert.ok(first);
    await row.getByLabel('Subtask title',{exact:true}).fill('Renamed browser pump');
    await reloadAfter(()=>row.getByRole('button',{name:'Save title'}).click());
    assert.equal(await page.locator('[data-subtask-id]').first().getAttribute('data-subtask-id'),first);
    await page.getByLabel('New Subtask title').fill('Browser valve');
    await reloadAfter(()=>page.getByRole('button',{name:'Add Subtask',exact:true}).click());
    await reloadAfter(()=>page.getByRole('button',{name:'Move up'}).click());
    assert.equal(await page.locator('[data-subtask-id]').last().getAttribute('data-subtask-id'),first);
    row=page.locator(`[data-subtask-id="${first}"]`);
    await reloadAfter(()=>row.getByRole('button',{name:'Complete',exact:true}).click());
    await row.getByRole('button',{name:'Reopen',exact:true}).waitFor();
    // Real mutation reaches the server; only its response is dropped. The page
    // must query the ORIGINAL receipt instead of toggling or replaying a new ID.
    // Intercept the ORIGINAL browser response after server acceptance. Reissuing
    // the POST with route.fetch changes transport details relevant to CSRF.
    const cdp=await context.newCDPSession(page);
    let lost=false;
    const interrupted=new Promise(resolve=>{
      cdp.on('Fetch.requestPaused',event=>{
        (async()=>{
          if(event.request.method==='POST'&&!lost){
            lost=true;
            let detail='';
            if(event.responseStatusCode!==200){const body=await cdp.send('Fetch.getResponseBody',{requestId:event.requestId});detail=body.base64Encoded?Buffer.from(body.body,'base64').toString():body.body;}
            await cdp.send('Fetch.failRequest',{requestId:event.requestId,errorReason:'ConnectionClosed'});
            resolve({status:event.responseStatusCode,detail});
          } else await cdp.send('Fetch.continueResponse',{requestId:event.requestId});
        })().catch(error=>resolve({status:0,detail:String(error)}));
      });
    });
    await cdp.send('Fetch.enable',{patterns:[{urlPattern:`${base}/jobs/${jobID}/subtasks`,requestStage:'Response'}]});
    await row.getByRole('button',{name:'Reopen',exact:true}).click();
    const response=await interrupted;assert.equal(response.status,200,response.detail);
    await page.getByText('Original operation: accepted. Reload to inspect current authorized checklist.',{exact:true}).waitFor();
    await cdp.send('Fetch.disable');await cdp.detach();await page.reload();
    await row.getByRole('button',{name:'Complete',exact:true}).waitFor();
    page.once('dialog',dialog=>dialog.dismiss());
    await row.getByRole('button',{name:'Remove Subtask'}).click();
    assert.equal(await row.locator('form[hx-confirm]').getAttribute('data-pending'),null,'cancelled removal must not publish');
    assert.equal(await row.count(),1);
    page.once('dialog',dialog=>dialog.accept());
    await reloadAfter(()=>row.getByRole('button',{name:'Remove Subtask'}).click());
    assert.equal(await page.locator(`[data-subtask-id="${first}"]`).count(),0);
    assert.equal(await page.locator('[data-subtask-id]').count(),1);
    const overflow=await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1);assert.equal(overflow,false,'320px page overflows');
    await page.keyboard.press('Tab');assert.ok(await page.evaluate(()=>document.activeElement!==document.body));
    if(process.env.SUBTASK_EVIDENCE_DIR){fs.mkdirSync(process.env.SUBTASK_EVIDENCE_DIR,{recursive:true});await page.screenshot({path:path.join(process.env.SUBTASK_EVIDENCE_DIR,'checklist-phone.png'),fullPage:true});}
    console.log(JSON.stringify({result:'passed',stable_identity:true,persisted_rename_reorder_complete_reopen_remove:true,lost_response_receipt:true,width:320}));
  } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
