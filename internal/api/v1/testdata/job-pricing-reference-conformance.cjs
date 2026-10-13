const assert = require('node:assert/strict');
const { JobPricingReferenceHarness } = require('./job-pricing-reference-harness.cjs');
const { ItemReferenceHarness } = require('./item-reference-harness.cjs');
(async()=>{
  const [base,token,jobText,itemText] = process.argv.slice(2);
  const job=Number(jobText), item=Number(itemText);
  const request=(path,method='GET')=>fetch(`${base}${path}`,{method,headers:{Authorization:`Bearer ${token}`}});
  const captures=new JobPricingReferenceHarness(id=>request(`/jobs/${id}`));
  const pricing=await captures.refresh(job);
  assert.equal(pricing.line_items[0].unit_price,31.25);
  assert.equal(pricing.line_items[0].title,'Reference captured work');
  assert.doesNotMatch(JSON.stringify(pricing),/unit_cost|987\.61/);
  const catalog=new ItemReferenceHarness(id=>request(`/items/${id}/selection`));
  await catalog.refresh(item);
  const selected=catalog.stageSelection(item,2);
  assert.equal(selected.unit_price,47.25);
  assert.equal(captures.current(job).line_items[0].unit_price,31.25,'catalog lookup must not reprice capture');
  for (const path of [`/jobs/${job}/pricing`,`/jobs/${job}/line-items`]) {
    const response=await request(path,'POST');
    assert.ok([404,405].includes(response.status),'Job planning must remain outside the field API');
  }
  for(const method of ['POST','PATCH','DELETE']) assert.equal((await request(`/items/${item}`,method)).status,405);
  assert.equal((await request('/session','DELETE')).status,204);
  await assert.rejects(()=>captures.refresh(job),/job_pricing_unavailable/);
  assert.equal(captures.current(job),null);
  await assert.rejects(()=>catalog.refresh(item),/selection_requires_review/);
  assert.throws(()=>catalog.stageSelection(item),/selection_unavailable/);
  console.log('PASS real bearer API: independent captured pricing, Cost-free authorized Item selection, no Job planning/catalog mutation, revocation purge');
})().catch(error=>{console.error(error);process.exitCode=1;});
