const assert=require('node:assert/strict');
const {chromium}=require('playwright-core');
(async()=>{
 const [base,cookie,job,customer,contact]=process.argv.slice(2);
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE||'/usr/bin/chromium-browser',args:['--no-sandbox']});
 const expired='Session expired—sign in again. Inaccessible input has been removed.';
 try {
  // Deferred scripts execute in document order. Either a cold interaction asset
  // or an earlier delayed Alpine asset must not change the security outcome.
  for(const asset of ['interaction.js','alpine.min.js']) {
   const context=await browser.newContext();await context.addCookies([{name:'session',value:cookie,url:base,httpOnly:true}]);
   const page=await context.newPage();const navigations=[],posts=[];
   page.on('request',r=>{if(r.method()==='POST')posts.push(new URL(r.url()).pathname);if(new URL(r.url()).pathname==='/login')navigations.push('/login');});
   await page.addInitScript(()=>{
    window.lookupStartupEvents=[];
    const original=window.fetch;
    window.fetch=function(...args){
     const url=new URL(args[0],location.href), lookup=url.pathname.endsWith('/options');
     if(lookup)window.lookupStartupEvents.push({phase:'dispatch',path:url.pathname,selected:url.searchParams.get('selected'),actions:!!window.freefsmActions});
     return original.apply(this,args).then(response=>{
      if(lookup){
       window.lookupStartupEvents.push({phase:'response',path:url.pathname,status:response.status});
       const text=response.text.bind(response);
       response.text=()=>text().then(body=>{window.lookupStartupEvents.push({phase:'body-read',path:url.pathname,status:response.status});return body;});
      }
      return response;
     });
    };
   });
   let releaseAsset,assetRequested,releaseExpiry,releaseProject;
   const assetHeld=new Promise(resolve=>releaseAsset=resolve),assetSeen=new Promise(resolve=>assetRequested=resolve);
   const expiryHeld=new Promise(resolve=>releaseExpiry=resolve),projectHeld=new Promise(resolve=>releaseProject=resolve);
   await page.route('**/static/js/'+asset+'*',async route=>{assetRequested();await assetHeld;await route.continue().catch(()=>{});});
   const contactPath=`/customers/${customer}/contacts/options`,projectPath=`/customers/${customer}/projects/options`;
   await page.route('**'+contactPath+'?*',async route=>{await expiryHeld;await route.fulfill({status:401,contentType:'text/html',body:'<h1>Untrusted expiry response</h1><option value="0">Untrusted option</option>'}).catch(()=>{});});
   await page.route('**'+projectPath+'?*',async route=>{
    // The late result is the actual authorized HTTP/DB response from this page's
    // request, not a fabricated cache entry or a response from another epoch.
    const response=await route.fetch();assert.equal(response.status(),200);
    await projectHeld;await route.fulfill({response}).catch(()=>{});
   });
   try {
    await page.goto(`${base}/jobs/${job}/edit`,{waitUntil:'commit'});await assetSeen;
    await page.waitForFunction(()=>document.readyState==='interactive'&&document.querySelector('#contact-select')?.dataset.lookupState==='loading');
    assert.equal(await page.evaluate(()=>!!window.freefsmActions),false,'the real shared guard is not installed while its deferred dependency is held');
    assert.equal(await page.locator('#contact-select').inputValue(),contact);
    assert.equal(await page.getByRole('button',{name:'Save',exact:true}).isDisabled(),true,'startup pending lookup must already block Save');
    assert.deepEqual(await page.evaluate(()=>window.lookupStartupEvents.filter(e=>e.phase==='dispatch')),[],'no lookup can outrun the deferred security guard');
    releaseAsset();await page.waitForLoadState('domcontentloaded');
    await page.waitForFunction(path=>window.lookupStartupEvents.some(e=>e.phase==='dispatch'&&e.path===path),projectPath);
    const dispatch=await page.evaluate(path=>window.lookupStartupEvents.filter(e=>e.phase==='dispatch'&&e.path===path),contactPath);
    assert.deepEqual(dispatch,[{phase:'dispatch',path:contactPath,selected:contact,actions:true}],'one current selected-Contact request starts with the real guard ready');
    releaseExpiry();await page.getByText(expired,{exact:true}).waitFor();
    assert.equal(new URL(page.url()).pathname,`/jobs/${job}/edit`);
    assert.equal(await page.locator('.page-body input,.page-body select,.page-body textarea,.page-body form').count(),0);
    assert.doesNotMatch(await page.locator('.page-body').innerText(),/Startup private|Untrusted/);
    releaseProject();
    await page.waitForFunction(path=>window.lookupStartupEvents.some(e=>e.phase==='body-read'&&e.path===path&&e.status===200),projectPath);
    assert.equal(await page.getByText(expired,{exact:true}).count(),1,'settled valid in-flight response cannot replace expiry');
    assert.equal(await page.locator('.page-body input,.page-body select,.page-body textarea,.page-body form').count(),0,'late choices cannot revive inaccessible input');
    assert.equal(await page.getByRole('link',{name:'Sign in',exact:true}).getAttribute('href'),'/login');
    assert.deepEqual(navigations,[],'401 uses the shared purge instead of a startup-dependent redirect');
    assert.deepEqual(posts,[],'neither expiry nor a late lookup submits a mutation');
    console.log(`PASS delayed ${asset}: lookup guard blocks before startup; current 401 preserves exact purge; real late 200 cannot revive input; zero POSTs`);
   }finally{releaseAsset();releaseExpiry();releaseProject();await context.close();}
  }
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
