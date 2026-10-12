const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright-core');
const { Builder, By, Key } = require('selenium-webdriver');
const firefox = require('selenium-webdriver/firefox');
const axe = require('axe-core');
const [base, cookie, customer, entry] = process.argv.slice(2);
const output = process.env.ACCESSIBILITY_OUTPUT || '/tmp/opencode/freefsm-ui-tools/evidence';
fs.mkdirSync(output, { recursive: true });
const report = { measurements: [], contrast: [], interactionStates: [], screenshots: [], violations: [] };
const paths = ['/customers', `/customers/${customer}`, '/customers/new', '/jobs/new', '/projects/new', '/assets/new', '/estimates/new', '/invoices/new', '/items/new', '/preferences', '/settings', '/settings/mail', '/settings/maps', '/settings/custom-fields/new', '/time-entries', '/users/new', '/tags', '/settings/assets', '/settings/statuses/job', '/schedule?tab=map'];
paths.push('/settings/authorization','/directory','/users','/settings/custom-fields','/activity',`/time-entries/${entry}`,`/time-entries/${entry}/edit`);
paths.push('/schedule?tab=list&period=day','/schedule?tab=calendar&period=week','/schedule?tab=dispatch&period=day');
paths.push('/settings/backup','/settings/backup/status-page');

// Measure painted text and actual hit rectangles. Inline prose links are exempt;
// navigation, icon links, buttons and controls are not.
function measurements() {
  const visible = el => { const r=el.getBoundingClientRect(); return r.width>0 && r.height>0 && getComputedStyle(el).visibility !== 'hidden' && !el.closest('[inert]'); };
  const targets = [...document.querySelectorAll('button,[role=button],summary,input:not([type=hidden]),select,textarea,nav a,.sidebar a')].filter(visible).filter(el => !el.disabled).map(el => {
    const label = el.matches('input[type=checkbox],input[type=radio]') && el.labels?.[0];
    const r = (label || el).getBoundingClientRect();
    return { name: (el.getAttribute('aria-label') || el.textContent || el.name).trim().slice(0, 65), width: r.width, height: r.height };
  });
  const rgb = color => { const c = document.createElement('canvas').getContext('2d'); c.fillStyle = color; c.fillRect(0,0,1,1); return [...c.getImageData(0,0,1,1).data].map((v,i) => i === 3 ? v/255 : v); };
  const blend = (c, bg) => c.slice(0,3).map((v,i) => v*c[3] + bg[i]*(1-c[3]));
  const background = el => !el ? [255,255,255] : blend(rgb(getComputedStyle(el).backgroundColor), background(el.parentElement));
  const luminance = c => c.map(v => v/255).map(v => v<=.04045 ? v/12.92 : ((v+.055)/1.055)**2.4).reduce((n,v,i) => n+v*[.2126,.7152,.0722][i],0);
  const contrast = (a,b) => { const x=luminance(a),y=luminance(b); return (Math.max(x,y)+.05)/(Math.min(x,y)+.05); };
  const boundaries = [...document.querySelectorAll('input:not([type=hidden]),select,textarea,button,[role=button]')].filter(visible).filter(el => !el.disabled).map(el => {
    const s=getComputedStyle(el), outer=background(el.parentElement), inner=background(el);
    const border=parseFloat(s.borderTopWidth)>0 ? blend(rgb(s.borderTopColor),outer) : outer;
    return {name: (el.getAttribute('aria-label') || el.textContent || el.name).trim().slice(0,65), field:el.matches('input,select,textarea'), ratio: Math.max(contrast(border,outer),contrast(inner,outer))};
  });
  return { width: innerWidth, height: innerHeight, dpr: devicePixelRatio, cssZoom: getComputedStyle(document.documentElement).zoom, visualScale: visualViewport.scale, scrollWidth: document.documentElement.scrollWidth, coarse: matchMedia('(any-pointer: coarse)').matches, targets, boundaries };
}
function check(m, label) {
  report.measurements.push({ label, ...m });
  if (m.scrollWidth > m.width + 1) report.violations.push({ label, reflow: m });
  const minimum = m.coarse ? 44 : 24;
  for (const t of m.targets) if (t.width < minimum - .1 || t.height < minimum - .1) report.violations.push({ label, target: t, minimum });
  // Text-only buttons are identified by text, rather than an enclosing shape.
  for (const boundary of m.boundaries) if ((boundary.field || boundary.ratio > 1.01) && boundary.ratio < 3) report.violations.push({label, boundary});
}
function recordContrast(result,label) {
  report.contrast.push({label,nodes:result.passes.filter(p=>p.id==='color-contrast').flatMap(p=>p.nodes.map(n=>({target:n.target,data:n.any.map(a=>a.data)})))});
  for(const violation of result.violations) report.violations.push({label,axe:violation.id,nodes:violation.nodes.map(n=>({target:n.target,summary:n.failureSummary}))});
}
function interactionTargets(route) {
  const primary=route==='/tags'?'a[href="/tags/new"][role=button]':route==='/settings/custom-fields'?'a[href="/settings/custom-fields/new"][role=button]':/^\/jobs\/\d+$/.test(route)?'form[action$="/create-estimate"] button':/^\/estimates\/\d+$/.test(route)?'form[action$="/convert"] button':null;
  return primary ? [primary,'.page-body a[href]:not([role=button])','#sidebar nav a.active'] : [];
}
async function chromiumStates(page,route,theme) {
  for(const selector of interactionTargets(route)) {
    const target=page.locator(selector).filter({visible:true}).first();
    if(!await target.count())continue;
    await target.evaluate(el=>el.setAttribute('data-measured-state',''));
    for(const state of ['hover','focus','active']) {
      await page.mouse.move(0,0);
      if(state==='focus')await target.focus();
      else {await target.evaluate(el=>el.blur());await target.hover();if(state==='active')await page.mouse.down();}
      assert.equal(await target.evaluate((el,state)=>state==='focus'?el===document.activeElement:el.matches(':'+state),state),true,'actual '+state+' state');
      const label=`Chromium ${theme} ${route} ${selector} ${state}`;
      recordContrast(await page.evaluate(async()=>axe.run('[data-measured-state]',{runOnly:['color-contrast']})),label);
      report.interactionStates.push(label);
      await page.mouse.move(0,0);if(state==='active')await page.mouse.up();
    }
    await target.evaluate(el=>{el.blur();el.removeAttribute('data-measured-state');});
  }
}
async function chromiumValidation(page,route,theme) {
  if(!['/customers/new','/settings'].includes(route))return;
  if(route==='/settings') {
    await page.locator('[name=business_name]').fill('Validation measurement');
    await page.locator('[name=timezone]').fill('Invalid/Test-Zone');
  }
  await page.getByRole('button',{name:route==='/settings'?'Save Company':'Save and continue',exact:true}).click();
  await page.waitForFunction(()=>document.activeElement?.hasAttribute('data-error-summary'));
  await page.evaluate(theme=>{document.documentElement.dataset.theme=theme;const p=document.querySelector('[data-theme-preference]');if(p)p.dataset.themePreference=theme;},theme);
  await page.mouse.move(0,0);
  await page.addScriptTag({content:axe.source});
  const label=`Chromium ${theme} ${route} ${route==='/settings'?'server422':'required-field'} validation`;
  recordContrast(await page.evaluate(async()=>axe.run(document,{runOnly:['color-contrast','label','link-name']})),label);
  report.interactionStates.push(label);check(await page.evaluate(measurements),label);
}
async function captureChromium(cdp,page,file) {
  // Playwright's clip coordinates are CSS pixels; native page zoom makes its
  // ordinary screenshot crop the physical surface. Capture the entire browser
  // surface directly, without clip/viewport/CSS-zoom emulation.
  const png=Buffer.from((await cdp.send('Page.captureScreenshot',{format:'png',fromSurface:true})).data,'base64');
  fs.writeFileSync(path.join(output,file),png);
  report.screenshots.push({file,width:png.readUInt32BE(16),height:png.readUInt32BE(20),...await page.evaluate(()=>({cssWidth:innerWidth,cssHeight:innerHeight,dpr:devicePixelRatio,cssZoom:getComputedStyle(document.documentElement).zoom}))});
}
async function chromiumChecks() {
  const extension = path.join(__dirname, 'native-zoom-extension');
  const context = await chromium.launchPersistentContext('', { executablePath: process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium-browser', headless: false, args: ['--no-sandbox', '--headless=new', '--disable-extensions-except=' + extension, '--load-extension=' + extension], viewport: null, reducedMotion: 'reduce' });
  try {
    await context.addCookies([{ name: 'session', value: cookie, url: base, httpOnly: true }]);
    const page = context.pages()[0];
    page.on('dialog',dialog=>dialog.accept());
    for (const kind of ['jobs','estimates','invoices']) {
      await page.goto(`${base}/${kind}/new`);
      await page.locator('[name=customer_id]').selectOption(customer);
      await page.locator(kind === 'jobs' ? '[name=job_type]' : '[name=title]').fill('Measured '+kind+' record');
      if (kind !== 'jobs') await page.getByRole('button',{name:'Remove line item',exact:true}).click();
      await page.getByRole('button',{name:'Save',exact:true}).click();
      await page.waitForURL(url=>!url.pathname.endsWith('/new'));
      const current=new URL(page.url()).pathname;
      const detailPath=/\/\d+$/.test(current) ? current : await page.getByText('Measured '+kind+' record',{exact:true}).first().getAttribute('href');
      paths.push(detailPath);
      if(kind==='jobs') {
        await page.goto(base+detailPath);
        let statusPosts=0; const observe=request=>{if(request.method()==='POST' && request.url().endsWith('/status'))statusPosts++;};page.on('request',observe);
        const select=page.locator('.job-status-select');
        await select.selectOption({index:1}); await page.waitForTimeout(80);
        assert.equal(statusPosts,0,'status selection requires explicit Save');
        const selected=await select.inputValue();
        await page.getByRole('button',{name:'Save status',exact:true}).click();
        await page.waitForFunction(()=>!document.querySelector('form.job-status-form[aria-busy=true]'));
        await page.reload(); assert.equal(await page.locator('.job-status-select').inputValue(),selected);
        page.off('request',observe);
      }
    }
    await page.goto(base+'/settings/maps');
    await page.locator('[name=map_tile_url]').fill(base+'/tiles/{z}/{x}/{y}.png');
    await page.getByRole('button',{name:'Save settings',exact:true}).click();
    await page.waitForURL(/flash=Settings/);
    for(const color of ['#ffffff','#000000','#ffff00']) {
      await page.goto(base+'/settings/statuses/job');
      await page.locator('[data-open-dialog="status-create-dialog"]').click();
      const form=page.locator('#status-create-dialog form');
      await form.locator('[name=name]').fill('Measured '+color);
      await form.locator('[name=color]').evaluate((input,color)=>{input.value=color;input.dispatchEvent(new Event('input',{bubbles:true}));},color);
      await form.getByRole('button',{name:'Create status',exact:true}).click();
      await page.locator('.status-chip-name').filter({hasText:'Measured '+color}).waitFor();
    }
    const worker = context.serviceWorkers()[0] || await context.waitForEvent('serviceworker');
    const cdp = await context.newCDPSession(page);
    const {windowId} = await cdp.send('Browser.getWindowForTarget');
    report.chromium = context.browser().version();
    for (const theme of ['light', 'dark']) {
      for (const route of paths) {
        assert.equal((await page.goto(base + route)).status(),200,route+' is a real installed authorized page');
        await page.evaluate(theme => { document.documentElement.dataset.theme = theme; const preference=document.querySelector('[data-theme-preference]');if(preference)preference.dataset.themePreference=theme; }, theme);
        if(route==='/jobs/new') for(const name of ['+ Add Visit','+ Add Assignment','+ Add Subtask']) await page.getByRole('button',{name,exact:true}).click();
        if(interactionTargets(route).length)await cdp.send('Browser.setWindowBounds',{windowId,bounds:{width:2000,height:1000}});
        await page.mouse.move(0,0);
        await page.addScriptTag({ content: axe.source });
        const result = await page.evaluate(async () => axe.run(document, { runOnly: ['color-contrast', 'label', 'button-name', 'link-name', 'select-name', 'aria-dialog-name'] }));
        report.contrast.push({label: `Chromium ${theme} ${route}`, nodes: result.passes.filter(p => p.id === 'color-contrast').flatMap(p => p.nodes.map(n => ({target:n.target, data:n.any.map(a=>a.data)})))});
        for (const violation of result.violations) report.violations.push({ label: `Chromium ${theme} ${route}`, axe: violation.id, nodes: violation.nodes.map(n => ({ target: n.target, summary: n.failureSummary })) });
        await chromiumStates(page,route,theme);
        if(route==='/settings/statuses/job') {
          await page.locator('.status-chip').filter({hasText:'Measured #ffffff'}).hover();
          const hover=await page.evaluate(async()=>axe.run(document,{runOnly:['color-contrast']}));
          for(const violation of hover.violations) report.violations.push({label:`Chromium ${theme} configured white status hover`,axe:violation.id,nodes:violation.nodes.map(n=>({target:n.target,summary:n.failureSummary}))});
          await page.mouse.move(0,0);
        }
        for (const size of [{width: 640, height: 960}, {width: 960, height: 640}]) {
          await cdp.send('Browser.setWindowBounds', { windowId, bounds: size });
          await worker.evaluate(async () => { const [tab] = await chrome.tabs.query({active: true}); await chrome.tabs.setZoom(tab.id, 2); });
          await page.waitForFunction(()=>devicePixelRatio===2);
          const m = await page.evaluate(measurements);
          assert.equal(m.dpr, 2, 'native Chromium zoom changes DPR');
          assert.equal(m.cssZoom, '1'); assert.equal(m.visualScale, 1);
          check(m, `Chromium ${theme} ${route} native200 ${size.width}x${size.height}`);
        }
        const selector = route === '/jobs/new' ? 'button[onclick="openJobCreateModal(\'project\')"]' : route === '/invoices/new' ? '.line-item-actions button:last-child' : route === '/settings/statuses/job' ? '[data-open-dialog]' : null;
        if (selector) {
          const opener=page.locator(selector).first(); await opener.click();
          const dialog=page.locator('dialog[open]'); await dialog.waitFor();
          const bounds=await dialog.evaluate(dialog=>{const r=(dialog.querySelector('article') || dialog).getBoundingClientRect();return {width:r.width,height:r.height,top:r.top,bottom:r.bottom,viewportWidth:innerWidth,viewportHeight:innerHeight}});
          assert.ok(bounds.width<=bounds.viewportWidth && bounds.top>=0 && bounds.bottom<=bounds.viewportHeight+1,JSON.stringify({route,bounds}));
          check(await page.evaluate(measurements),`Chromium ${theme} ${route} focused dialog native200`);
          const result=await page.evaluate(async()=>axe.run(document,{runOnly:['color-contrast','label','button-name','select-name','aria-dialog-name']}));
          for(const violation of result.violations) report.violations.push({label:`Chromium ${theme} ${route} dialog`,axe:violation.id,nodes:violation.nodes.map(n=>({target:n.target,summary:n.failureSummary}))});
          await captureChromium(cdp,page,`chromium-${theme}-${route.split('/')[1]}-dialog-native200.png`);
          await page.keyboard.press('Escape');
          assert.equal(await opener.evaluate(el=>el===document.activeElement),true,'dialog closure restores initiating control');
        }
        await chromiumValidation(page,route,theme);
      }
      await page.goto(base+'/invoices/new');
      await page.evaluate(theme=>{document.documentElement.dataset.theme=theme;document.querySelector('[data-theme-preference]').dataset.themePreference=theme},theme);
      await page.evaluate(()=>scrollTo(0,0));
      await captureChromium(cdp,page,`chromium-${theme}-native200.png`);
      await page.locator('.entity-form-actions').scrollIntoViewIfNeeded();
      await captureChromium(cdp,page,`chromium-${theme}-native200-actions.png`);
    }
    // A touch-capable laptop is covered by any-pointer, without a mobile UA.
    await worker.evaluate(async () => { const [tab] = await chrome.tabs.query({active: true}); await chrome.tabs.setZoom(tab.id, 1); });
    const touch = await context.browser().newContext({ viewport: {width: 320, height: 640}, hasTouch: true, reducedMotion: 'reduce' });
    await touch.addCookies([{ name: 'session', value: cookie, url: base }]);
    const phone = await touch.newPage();
    phone.on('dialog',dialog=>dialog.accept());
    for (const route of paths) {
      await phone.goto(base + route);
      if(route==='/jobs/new') for(const name of ['+ Add Visit','+ Add Assignment','+ Add Subtask']) await phone.getByRole('button',{name,exact:true}).click();
      for(const theme of ['light','dark']) {
        await phone.evaluate(theme=>{document.documentElement.dataset.theme=theme;const preference=document.querySelector('[data-theme-preference]');if(preference)preference.dataset.themePreference=theme},theme);
        for(const size of [{width:320,height:640},{width:640,height:320},{width:1280,height:800}]) {
          await phone.setViewportSize(size);
          await phone.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
          check(await phone.evaluate(measurements),`Chromium ${theme} coarse ${size.width}x${size.height} ${route}`);
        }
      }
    }
    await phone.setViewportSize({width:320,height:640});
    await phone.screenshot({path: path.join(output, 'chromium-coarse320.png'), fullPage: true});
    await phone.goto(base+'/schedule?tab=map');
    const map=phone.locator('.leaflet-container');
    await map.waitFor(); await map.focus();
    const before=await phone.locator('.leaflet-map-pane').evaluate(el=>getComputedStyle(el).transform);
    await phone.keyboard.press('ArrowRight');
    const positions=await phone.locator('.leaflet-map-pane').evaluate(async el=>{const positions=[];for(let i=0;i<8;i++){await new Promise(requestAnimationFrame);positions.push(getComputedStyle(el).transform);}return positions;});
    assert.notEqual(positions[0],before,'map has keyboard pan alternative');
    assert.equal(new Set(positions).size,1,'reduced-motion map pan settles immediately without animation');
    report.reducedMotion={engine:'Chromium',keyboardPan:true,positions};
    await touch.close();
  } finally { await context.close(); }
}
async function firefoxChecks() {
  const driverPath = process.env.GECKODRIVER_PATH;
  assert.ok(driverPath, 'set GECKODRIVER_PATH to isolated dev driver');
  const driver = await new Builder().forBrowser('firefox').setFirefoxService(new firefox.ServiceBuilder(driverPath)).setFirefoxOptions(new firefox.Options().setBinary(process.env.FIREFOX_EXECUTABLE || '/usr/bin/firefox').addArguments('-headless', '--remote-allow-system-access').setPreference('ui.prefersReducedMotion', 1).set('unhandledPromptBehavior','accept')).build();
  try {
    report.firefox = (await driver.getCapabilities()).get('browserVersion');
    await driver.get(base + '/login');
    await driver.manage().addCookie({name: 'session', value: cookie, httpOnly: true});
    for (const theme of ['light', 'dark']) {
      for (const route of paths) {
        await driver.get(base + route);
        await driver.executeScript('document.documentElement.dataset.theme=arguments[0]; const preference=document.querySelector("[data-theme-preference]");if(preference)preference.dataset.themePreference=arguments[0]', theme);
        if(route==='/jobs/new') for(const name of ['+ Add Visit','+ Add Assignment','+ Add Subtask']) await driver.findElement(By.xpath(`//button[normalize-space(.)='${name}']`)).click();
        if(interactionTargets(route).length)await driver.manage().window().setRect({width:2000,height:1000});
        await driver.actions().move({x:0,y:0,origin:'viewport'}).perform();
        await driver.executeScript(axe.source);
        const result = await driver.executeAsyncScript("const done=arguments[arguments.length-1]; axe.run(document,{runOnly:['color-contrast','label','button-name','link-name','select-name','aria-dialog-name']}).then(done)");
        report.contrast.push({label: `Firefox ${theme} ${route}`, nodes: result.passes.filter(p => p.id === 'color-contrast').flatMap(p => p.nodes.map(n => ({target:n.target, data:n.any.map(a=>a.data)})))});
        for (const violation of result.violations) report.violations.push({ label: `Firefox ${theme} ${route}`, axe: violation.id, nodes: violation.nodes.map(n => ({ target: n.target, summary: n.failureSummary })) });
        for(const selector of interactionTargets(route)) {
          const targets=await driver.findElements(By.css(selector));
          let target;
          for(const candidate of targets)if(await candidate.isDisplayed()){target=candidate;break;}
          if(!target)continue;
          await driver.executeScript('arguments[0].setAttribute("data-measured-state","");arguments[0].scrollIntoView({block:"center"})',target);
          for(const state of ['hover','focus','active']) {
            await driver.actions().move({x:0,y:0,origin:'viewport'}).perform();
            if(state==='focus')await driver.executeScript('arguments[0].focus()',target);
            else {await driver.executeScript('arguments[0].blur()',target);await driver.actions().move({origin:target}).perform();if(state==='active')await driver.actions().press().perform();}
            assert.equal(await driver.executeScript('return arguments[1]==="focus"?arguments[0]===document.activeElement:arguments[0].matches(":"+arguments[1])',target,state),true,'Firefox actual '+state);
            const label=`Firefox ${theme} ${route} ${selector} ${state}`;
            const measured=await driver.executeAsyncScript('const done=arguments[arguments.length-1];axe.run("[data-measured-state]",{runOnly:["color-contrast"]}).then(done)');
            recordContrast(measured,label);report.interactionStates.push(label);
            await driver.actions().move({x:0,y:0,origin:'viewport'}).perform();if(state==='active')await driver.actions().release().perform();
          }
          await driver.executeScript('arguments[0].blur();arguments[0].removeAttribute("data-measured-state")',target);
        }
        if(route==='/settings/statuses/job') {
          const white=await driver.findElement(By.xpath("//button[contains(@class,'status-chip') and contains(.,'Measured #ffffff')]"));
          await driver.executeScript('arguments[0].scrollIntoView({block:"center"})',white);
          await driver.actions().move({origin:white}).perform();
          const hover=await driver.executeAsyncScript("const done=arguments[arguments.length-1];axe.run(document,{runOnly:['color-contrast']}).then(done)");
          for(const violation of hover.violations) report.violations.push({label:`Firefox ${theme} configured white status hover`,axe:violation.id,nodes:violation.nodes.map(n=>({target:n.target,summary:n.failureSummary}))});
          await driver.actions().move({x:0,y:0,origin:'viewport'}).perform();
        }
        for (const size of [{width: 640, height: 960}, {width: 960, height: 640}]) {
          await driver.manage().window().setRect(size);
          await driver.setContext('chrome');
          await driver.executeScript('FullZoom.setZoom(2)');
          await driver.setContext('content');
          await driver.wait(()=>driver.executeScript('return devicePixelRatio === 2'),5000);
          const m = await driver.executeScript(measurements);
          assert.equal(m.dpr, 2, 'native Firefox page zoom changes DPR');
          assert.equal(m.cssZoom, '1'); assert.equal(m.visualScale, 1);
          check(m, `Firefox ${theme} ${route} native200 ${size.width}x${size.height}`);
        }
        const selector = route === '/jobs/new' ? 'button[onclick="openJobCreateModal(\'project\')"]' : route === '/invoices/new' ? '.line-item-actions button:last-child' : route === '/settings/statuses/job' ? '[data-open-dialog]' : null;
        if(selector) {
          const opener=await driver.findElement(By.css(selector)); await opener.click();
          await driver.wait(()=>driver.executeScript('return !!document.querySelector("dialog[open]")'),5000);
          check(await driver.executeScript(measurements),`Firefox ${theme} ${route} focused dialog native200`);
          const result=await driver.executeAsyncScript("const done=arguments[arguments.length-1]; axe.run(document,{runOnly:['color-contrast','label','button-name','select-name','aria-dialog-name']}).then(done)");
          for(const violation of result.violations) report.violations.push({label:`Firefox ${theme} ${route} dialog`,axe:violation.id,nodes:violation.nodes.map(n=>({target:n.target,summary:n.failureSummary}))});
          fs.writeFileSync(path.join(output,`firefox-${theme}-${route.split('/')[1]}-dialog-native200.png`),await driver.takeScreenshot(),'base64');
          await driver.actions().sendKeys(Key.ESCAPE).perform();
          assert.equal(await driver.executeScript('return arguments[0]===document.activeElement',opener),true,'Firefox dialog closure restores focus');
        }
        if(['/customers/new','/settings'].includes(route)) {
          if(route==='/settings')for(const [name,value] of [['business_name','Validation measurement'],['timezone','Invalid/Test-Zone']]){const input=await driver.findElement(By.css(`[name=${name}]`));await input.clear();await input.sendKeys(value);}
          const caption=route==='/settings'?'Save Company':'Save and continue';
          await driver.findElement(By.xpath(`//button[normalize-space(.)='${caption}']`)).click();
          await driver.wait(()=>driver.executeScript('return document.activeElement?.hasAttribute("data-error-summary")'),5000);
          await driver.executeScript('document.documentElement.dataset.theme=arguments[0];const p=document.querySelector("[data-theme-preference]");if(p)p.dataset.themePreference=arguments[0]',theme);
          await driver.executeScript(axe.source);
          await driver.actions().move({x:0,y:0,origin:'viewport'}).perform();
          const label=`Firefox ${theme} ${route} ${route==='/settings'?'server422':'required-field'} validation`;
          recordContrast(await driver.executeAsyncScript('const done=arguments[arguments.length-1];axe.run(document,{runOnly:["color-contrast","label","link-name"]}).then(done)'),label);
          report.interactionStates.push(label);check(await driver.executeScript(measurements),label);
        }
      }
      await driver.get(base+'/invoices/new');
      await driver.executeScript('document.documentElement.dataset.theme=arguments[0];document.querySelector("[data-theme-preference]").dataset.themePreference=arguments[0]',theme);
      fs.writeFileSync(path.join(output, `firefox-${theme}-native200.png`), await driver.takeScreenshot(), 'base64');
    }
    await driver.get(base+'/schedule?tab=map');
    const map=await driver.wait(()=>driver.findElement(By.css('.leaflet-container')),5000);
    await driver.executeScript('arguments[0].focus()',map);
    const before=await driver.executeScript('return getComputedStyle(document.querySelector(".leaflet-map-pane")).transform');
    await driver.actions().sendKeys(Key.ARROW_RIGHT).perform();
    const positions=await driver.executeAsyncScript('const done=arguments[arguments.length-1],el=document.querySelector(".leaflet-map-pane"),positions=[];function frame(){positions.push(getComputedStyle(el).transform); if(positions.length===8)done(positions);else requestAnimationFrame(frame)}requestAnimationFrame(frame)');
    assert.notEqual(positions[0],before); assert.equal(new Set(positions).size,1,'Firefox reduced-motion keyboard map pan is immediate');
    report.reducedMotionFirefox={keyboardPan:true,positions};
  } finally { await driver.quit(); }
}
(async () => {
  try { await chromiumChecks(); await firefoxChecks(); }
  finally {
    const text=report.contrast.flatMap(c=>c.nodes.flatMap(n=>n.data)).filter(d=>Number.isFinite(d?.contrastRatio));
    report.summary={pages:paths.length,measurements:report.measurements.length,interactionStates:report.interactionStates.length,textMeasurements:text.length,
      minimumNormalText:Math.min(...text.filter(d=>d.expectedContrastRatio==='4.5:1').map(d=>d.contrastRatio)),
      minimumLargeText:Math.min(...text.filter(d=>d.expectedContrastRatio==='3:1').map(d=>d.contrastRatio)),
      minimumControlBoundary:Math.min(...report.measurements.flatMap(m=>m.boundaries).filter(b=>b.field||b.ratio>1.01).map(b=>b.ratio)),
      minimumFineTarget:Math.min(...report.measurements.filter(m=>!m.coarse).flatMap(m=>m.targets.flatMap(t=>[t.width,t.height]))),
      minimumCoarseTarget:Math.min(...report.measurements.filter(m=>m.coarse).flatMap(m=>m.targets.flatMap(t=>[t.width,t.height])))};
    fs.writeFileSync(path.join(output, 'accessibility.json'), JSON.stringify(report, null, 2));
    fs.writeFileSync(path.join(output, 'summary.json'), JSON.stringify({chromium:report.chromium,firefox:report.firefox,...report.summary,violations:report.violations.length,reducedMotion:report.reducedMotion,reducedMotionFirefox:report.reducedMotionFirefox},null,2));
  }
  console.log(JSON.stringify({chromium: report.chromium, firefox: report.firefox, summary:report.summary, violations: report.violations.slice(0,20).map(v=>({...v,nodes:v.nodes?.slice(0,3)})), violationCount: report.violations.length, output}, null, 2));
  assert.equal(report.violations.length, 0, 'rendered accessibility requirements');
})().catch(error => { console.error(error); process.exitCode = 1; });
