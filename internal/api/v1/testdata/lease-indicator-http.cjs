'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const {join}=require('node:path');
const {randomUUID}=require('node:crypto');
const {FieldLeaseClient,ProtectedStore}=require('../reference/lease-indicator-client.cjs');
(async()=>{
 const input=JSON.parse(fs.readFileSync(0,'utf8'));
 const store=new ProtectedStore(input.directory,randomUUID());
 let offline=false,fail=false,offset=0,refreshFailure=null;
 const transport=async(path,method,body,auth)=>{
  if(offline)throw Error('offline');
  if(refreshFailure&&path.endsWith(refreshFailure.path)){
   if(refreshFailure.transport)throw Error('refresh_transport_failure');
   return {status:503,error:{code:'retryable_failure'}};
  }
  if(fail&&path.endsWith('/master-context'))return {status:503,error:{code:'retryable_failure'}};
  const headers={Authorization:'Bearer '+auth.token};
  if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;
  if(auth.operation)headers['Operation-ID']=auth.operation;
  if(body!==undefined)headers['Content-Type']='application/json';
   const request=()=>fetch(input.url+path,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
   let r=await request();
   if(r.status===429){
    const limited=await r.json();
    assert.equal(limited.error.retry_after_seconds,60);
    console.log('Production request budget: waiting indicated 60 seconds before one retry');
    await new Promise(resolve=>setTimeout(resolve,60000));
    r=await request();
   }
   const result={status:r.status,...await r.json()};
   if(path.startsWith('/sync/jobs'))assert.equal(r.status,200,JSON.stringify(result));
   return result;
 };
 let client=new FieldLeaseClient(store,transport,()=>Date.now()+offset);
 try{
  await client.initialize();await client.authenticate(input.token);await client.synchronize();
    const initialCache=(await client.view()).cache;
    const job=Object.keys(initialCache).find(id=>initialCache[id].title==='Reference job');
    const otherJob=Object.keys(initialCache).find(id=>initialCache[id].title==='Independent cached job');
    assert(job&&otherJob&&job!==otherJob);
    refreshFailure={path:'/offline/policy',transport:true};
    await assert.rejects(client.readLeaseIndicators(input.job),/inaccessible_target/);
    assert.deepEqual((await client.view()).cache,initialCache,'unknown cache keys cannot retarget a Job');
    refreshFailure=null;
    const failures=[];
    const observations=value=>{
     if(!value||typeof value!=='object')return [];
     return Object.entries(value).flatMap(([key,child])=>key==='lease_coverage'?[child]:observations(child));
    };
    const facts=record=>JSON.parse(JSON.stringify(record,(key,value)=>['lease_coverage','lease_indicators'].includes(key)?undefined:value));
    const checkRefreshFailures=async expected=>{
    for(const failure of [
     {path:'/offline/policy',error:/reauthorization_unavailable/},
     {path:'/offline/policy',transport:true,error:/refresh_transport_failure/},
     {path:'/master-context',error:/indicator_unavailable/},
     {path:'/master-context',transport:true,error:/refresh_transport_failure/}
     ]){
      await client.synchronize(failure.transport===true);
      const synced=(await client.view()).cache[job];
      let otherBefore=(await client.view()).cache[otherJob];
      const opaqueAsset=synced.masters.asset.id;
      assert.equal(typeof opaqueAsset,'string');
      assert.notEqual(opaqueAsset,input.asset);
      assert.equal((await client.leaseIndicator(job,opaqueAsset)).value,expected,'sync restores opaque observation');
      assert.equal((await client.leaseIndicator(job,input.asset)).value,'Unavailable','sync does not guess numeric aliases');
      assert.equal(synced.masters.asset.service_entries[0].summary,'Retained authorized service fact');
      assert.equal(observations(synced).length,4,'singleton, collection, and both nested Service Entry copies populated');
      for(const c of observations(synced))assert.equal(c.covered,expected==='Yes');
      await client.readLeaseIndicators(job);
      assert.equal((await client.leaseIndicator(job,input.asset)).value,expected);
      assert.equal((await client.leaseIndicator(job,opaqueAsset)).value,'Unavailable','direct refresh does not misassociate numeric and opaque IDs');
      assert.equal(observations((await client.view()).cache[job]).length,0,'successful direct refresh also retires old synchronized observations');
     for(const synchronized of [false,true]){
      if(synchronized){
       await client.synchronize(true);
       const restored=(await client.view()).cache;
       assert.equal(observations(restored[job]).length,4,'failed refresh starts with populated synchronized observations too');
       assert.equal((await client.leaseIndicator(job,opaqueAsset)).value,expected);
       otherBefore=restored[otherJob];
      }
     refreshFailure=failure;
     await assert.rejects(client.readLeaseIndicators(job),failure.error);
     for(const restarted of [false,true]){
      if(restarted)client=new FieldLeaseClient(new ProtectedStore(input.directory,store.id),transport,()=>Date.now()+offset);
      const observed=await client.leaseIndicator(job,input.asset);
      if(observed.value!=='Unavailable')failures.push({expected,prerequisite:failure.path,transport:!!failure.transport,restarted,actual:observed.value});
       const view=await client.view();
       assert.deepEqual(facts(view.cache[job]),facts(synced),'all unrelated authorized facts/history retained');
       assert.deepEqual(view.cache[otherJob],otherBefore,'refresh cannot invalidate another Job even for the same Asset');
       assert.equal((await client.leaseIndicator(job,opaqueAsset)).value,'Unavailable');
       if(view.cache[job].lease_indicators)failures.push({expected,prerequisite:failure.path,restarted,cache:'retained'});
       if(observations(view.cache[job]).length)failures.push({expected,prerequisite:failure.path,restarted,synchronized:'retained'});
       assert.equal(view.cache[job].masters.asset.service_entries[0].summary,'Retained authorized service fact');
     }
     refreshFailure=null;
     }
     }
     assert.deepEqual(failures,[],'all public observations unavailable after failed refresh');
     await client.readLeaseIndicators(job);
   };
   await checkRefreshFailures('No');
   // A second client cannot observe or republish the old value while the first
   // client is refreshing. The real store's durable writer lock owns the round trip.
   let entered,release;
   const contacting=new Promise(resolve=>{entered=resolve});
   const response=new Promise(resolve=>{release=resolve});
   const refreshing=new FieldLeaseClient(new ProtectedStore(input.directory,store.id),async(path,...args)=>{
    if(path==='/offline/policy'){entered();return response;}
    return transport(path,...args);
   },()=>Date.now()+offset);
   const refresh=assert.rejects(refreshing.readLeaseIndicators(job),/reauthorization_unavailable/);
   await contacting;
   try{
    await assert.rejects(client.leaseIndicator(job,input.asset),/store_busy/);
     await assert.rejects(client.readLeaseIndicators(job),/store_busy/);
     await assert.rejects(client.view(),/store_busy/);
     await assert.rejects(client.synchronize(true),/store_busy/);
   }finally{release({status:503});await refresh;}
   assert.equal((await client.leaseIndicator(job,input.asset)).value,'Unavailable');
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
     await checkRefreshFailures(expected);
    }
   assert.deepEqual(failures,[],'failed refresh must withhold persisted Yes/No immediately and after restart');
  offline=false;fail=true;
  await assert.rejects(client.readLeaseIndicators(job),/indicator_unavailable/);
  assert.equal((await client.leaseIndicator(job,input.asset)).value,'Unavailable');
   fail=false;await client.readLeaseIndicators(job);
   // Fail the actual filesystem rename during invalidation, after the access
   // high-water write. Old ciphertext must remain locked even after disk repair.
   const directory=fs.mkdtempSync(join(input.directory,'purge-failure-'));
   const faultStore=new ProtectedStore(directory,randomUUID());
   let contacts=0;
   const faultClient=new FieldLeaseClient(faultStore,async(...args)=>{contacts++;return transport(...args);},()=>Date.now()+offset);
   const disk=require('node:fs/promises'),rename=disk.rename;
   try{
    await faultClient.initialize();await faultClient.authenticate(input.token);await faultClient.synchronize();await faultClient.readLeaseIndicators(job);
    assert.equal((await faultClient.leaseIndicator(job,input.asset)).value,'No');
    let writes=0;contacts=0;
    disk.rename=async(from,to)=>{
     if(to===join(directory,'cache')&&++writes===2){
      await rename(to,join(directory,'cache-held'));await disk.mkdir(to);
     }
     return rename(from,to);
    };
    try{await assert.rejects(faultClient.readLeaseIndicators(job),/protected_store_locked/);}
    finally{disk.rename=rename;}
    assert.equal(contacts,0,'failed invalidation must stop before network prerequisites');
    await disk.rmdir(join(directory,'cache'));await rename(join(directory,'cache-held'),join(directory,'cache'));
    await assert.rejects(faultClient.leaseIndicator(job,input.asset),/store_busy/);
    const restarted=new FieldLeaseClient(new ProtectedStore(directory,faultStore.id),transport,()=>Date.now()+offset);
    await assert.rejects(restarted.leaseIndicator(job,input.asset),/store_busy/);
     await assert.rejects(restarted.readLeaseIndicators(job),/store_busy/);
     await assert.rejects(restarted.view(),/store_busy/);
     await assert.rejects(restarted.synchronize(true),/store_busy/);
   }finally{disk.rename=rename;await faultStore.destroy();}
   await fetch(input.url+'/__fixture/revoke',{method:'POST'});
  await client.synchronize();
  assert.equal((await client.leaseIndicator(job,input.asset)).value,'Unavailable');
  assert(!JSON.stringify(await store.load()).includes('lease_indicators'));
 }finally{await store.destroy();}
})().catch(e=>{console.error(e);process.exit(1)});
