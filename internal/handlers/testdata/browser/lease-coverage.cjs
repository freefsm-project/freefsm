const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
const [base, cookie, lease, outgoing, incoming, today] = process.argv.slice(2);
function day(offset) { const d = new Date(today+'T12:00:00Z'); d.setUTCDate(d.getUTCDate()+offset); return d.toISOString().slice(0,10); }
(async () => {
 const browser = await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
 try {
  const context = await browser.newContext({viewport:{width:390,height:844}});
  await context.addCookies([{name:'session',value:cookie,url:base}]);
  const page=await context.newPage();
  const path=base+`/leases/${lease}/coverage`;
  async function open(){const r=await page.goto(path);assert.equal(r.status(),200);}
  async function save(form){await Promise.all([page.waitForNavigation(),form.getByRole('button',{name:'Save coverage change'}).click()]);assert.match(await page.locator('h1').innerText(),/Coverage changes/);}
  await open();
  let form=page.locator('form').filter({has:page.locator('input[name="action"][value="create"]')});
  await form.locator('[name="kind"]').selectOption('replace');
  await form.locator('[name="effective_on"]').fill(day(10));
  await form.locator('[name="outgoing_id"]').fill(incoming);
  await form.locator('[name="incoming_id"]').selectOption(outgoing);
  await form.locator('[name="reason"]').fill('Scheduled browser replacement');
  await save(form);
  let section=page.locator('section').filter({hasText:'Scheduled browser replacement'});
  form=section.locator('form');
  await form.locator('[name="effective_on"]').fill(day(11));
  await form.locator('[name="reason"]').fill('Revised appointment date');
  await save(form);
  section=page.locator('section').filter({hasText:'Revised appointment date'});
  form=section.locator('form');
  await form.locator('[name="action"]').selectOption('withdraw');
  await form.locator('[name="reason"]').fill('Customer withdrew appointment');
  await save(form);
  form=page.locator('form').filter({has:page.locator('input[name="action"][value="create"]')});
  await form.locator('[name="kind"]').selectOption('add');
  await form.locator('[name="effective_on"]').fill(day(12));
  await form.locator('[name="incoming_id"]').selectOption(outgoing);
  await form.locator('[name="rent_cents"]').fill('1700');
  await form.locator('[name="charge_cents"]').fill('500');
  await form.locator('[name="tax_rate"]').fill('0');
  await form.locator('[name="reason"]').fill('Add spare equipment');
  await save(form);
  form=page.locator('form').filter({has:page.locator('input[name="action"][value="create"]')});
  await form.locator('[name="kind"]').selectOption('remove');
  await form.locator('[name="effective_on"]').fill(day(14));
  await form.locator('[name="outgoing_id"]').fill(outgoing);
  await form.locator('[name="reason"]').fill('Later real removal');
  await save(form);
  section=page.locator('section').filter({hasText:'Equipment replacement'});
  form=section.locator('form');
  await form.locator('[name="effective_on"]').fill(day(-1));
  await form.locator('[name="reason"]').fill('Correct factual replacement date');
  await save(form);
  const text=await page.locator('body').innerText();
  for(const reason of ['Scheduled browser replacement','Revised appointment date','Customer withdrew appointment','Correct factual replacement date','Later real removal']) assert.ok(text.includes(reason));
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
  console.log('PASS phone browser Add/Remove/Replace, pending revise/withdraw, factual correction and immutable history');
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
