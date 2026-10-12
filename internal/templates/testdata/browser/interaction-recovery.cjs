const assert=require('node:assert/strict');
const {chromium}=require('playwright-core');
(async()=>{
 const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE || '/usr/bin/chromium-browser',args:['--no-sandbox']});
 try {
  const page=await browser.newPage();let posts=0;page.on('request',request=>{if(request.method()==='POST')posts++;});await page.goto(process.argv[2]+'/customers/new');
  await page.locator('[name=display_name]').fill('Private expired-session input');
  await page.getByRole('button',{name:'Save and continue',exact:true}).click();
  await page.getByText('Session expired—sign in again. Inaccessible input has been removed.',{exact:true}).waitFor();
  assert.equal(await page.locator('[name=display_name]').count(),0);
  const notices=JSON.parse(await page.evaluate(()=>sessionStorage.getItem('freefsm-unresolved-actions')));
  assert.equal(notices.length,1);assert.equal(notices[0].state,'unknown');
  assert.doesNotMatch(JSON.stringify(notices),/Private|display_name|password|csrf/);
  await page.getByRole('link',{name:'Sign in',exact:true}).click();
  await page.waitForURL('**/login');
  await page.getByText('An earlier action still needs reconciliation. No input has been restored.',{exact:false}).waitFor();
  assert.equal(await page.locator('[name=display_name]').inputValue(),'');
  assert.equal(posts,1,'session failure and return never replay the mutation');
  console.log('PASS expired-session response removes the actual form input while retaining only opaque unresolved metadata across explicit navigation; no restored draft or automatic replay');
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
