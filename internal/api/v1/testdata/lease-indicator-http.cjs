'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const {join}=require('node:path');
const {randomUUID}=require('node:crypto');
const {FieldLeaseClient,ProtectedStore}=require('../reference/lease-indicator-client.cjs');
(async()=>{
 const input=JSON.parse(fs.readFileSync(0,'utf8'));
 const store=new ProtectedStore(input.directory,randomUUID());
 let offline=false,fail=false,offset=0;
 const transport=async(path,method,body,auth)=>{
  if(offline)throw Error('offline');
  if(fail&&path.endsWith('/master-context'))return {status:503,error:{code:'retryable_failure'}};
  const headers={Authorization:'Bearer '+auth.token};
  if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;
  if(auth.operation)headers['Operation-ID']=auth.operation;
  if(body!==undefined)headers['Content-Type']='application/json';
  const r=await fetch(input.url+path,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
  return {status:r.status,...await r.json()};
 };
 let client=new FieldLeaseClient(store,transport,()=>Date.now()+offset);
 try{
  await client.initialize();await client.authenticate(input.token);await client.synchronize();
  const job=Object.keys((await client.view()).cache)[0];
  await client.readLeaseIndicators(job);
  const initial=await client.leaseIndicator(job,input.asset);
  assert.equal(initial.value,'No');assert.match(initial.label,/Last synced.*as of/);
  assert(!fs.readFileSync(join(input.directory,'cache')).includes(initial.label));
  offline=true;offset+=3600000;
  client=new FieldLeaseClient(new ProtectedStore(input.directory,store.id),transport,()=>Date.now()+offset);
  assert.deepEqual(await client.leaseIndicator(job,input.asset),initial,'restart/date crossing cannot recompute coverage');
   const {chromium}=require('playwright-core');
  const browser=await chromium.launch({executablePath:process.env.BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox']});
   try{const page=await browser.newPage();await page.setContent('<output aria-label="Lease coverage"></output>');await page.locator('output').evaluate((e,text)=>{e.textContent=text},initial.label);assert.equal(await page.locator('output').innerText(),initial.label);}finally{await browser.close();}
   for(const [transition,expected] of [['start','Yes'],['renew','Yes'],['terminate','No']]){
    const before=await client.leaseIndicator(job,input.asset);
    offline=true;
    assert.equal((await fetch(input.url+'/__fixture/'+transition,{method:'POST'})).status,204);
    offset+=3600000;
    client=new FieldLeaseClient(new ProtectedStore(input.directory,store.id),transport,()=>Date.now()+offset);
    assert.deepEqual(await client.leaseIndicator(job,input.asset),before,transition+' offline retains exact observation and as-of');
    offline=false;await client.readLeaseIndicators(job);
    assert.equal((await client.leaseIndicator(job,input.asset)).value,expected,transition+' online observes current domain state');
   }
  offline=false;fail=true;
  await assert.rejects(client.readLeaseIndicators(job),/indicator_unavailable/);
  assert.equal((await client.leaseIndicator(job,input.asset)).value,'Unavailable');
  fail=false;await client.readLeaseIndicators(job);
  await fetch(input.url+'/__fixture/revoke',{method:'POST'});
  await client.synchronize();
  assert.equal((await client.leaseIndicator(job,input.asset)).value,'Unavailable');
  assert(!JSON.stringify(await store.load()).includes('lease_indicators'));
 }finally{await store.destroy();}
})().catch(e=>{console.error(e);process.exit(1)});
