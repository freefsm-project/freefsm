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
  const r=await fetch(input.url+path,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
  return {status:r.status,...await r.json()};
 };
 let client=new FieldLeaseClient(store,transport,()=>Date.now()+offset);
 try{
  await client.initialize();await client.authenticate(input.token);await client.synchronize();
   const job=Object.keys((await client.view()).cache)[0];
   const failures=[];
   const checkRefreshFailures=async expected=>{
    for(const failure of [
     {path:'/offline/policy',error:/reauthorization_unavailable/},
     {path:'/offline/policy',transport:true,error:/refresh_transport_failure/},
     {path:'/master-context',error:/indicator_unavailable/},
     {path:'/master-context',transport:true,error:/refresh_transport_failure/}
    ]){
     await client.readLeaseIndicators(job);
     assert.equal((await client.leaseIndicator(job,input.asset)).value,expected);
     refreshFailure=failure;
     await assert.rejects(client.readLeaseIndicators(job),failure.error);
     for(const restarted of [false,true]){
      if(restarted)client=new FieldLeaseClient(new ProtectedStore(input.directory,store.id),transport,()=>Date.now()+offset);
      const observed=await client.leaseIndicator(job,input.asset);
      if(observed.value!=='Unavailable')failures.push({expected,prerequisite:failure.path,transport:!!failure.transport,restarted,actual:observed.value});
      const view=await client.view();
      if(view.cache[job].lease_indicators)failures.push({expected,prerequisite:failure.path,restarted,cache:'retained'});
     }
     refreshFailure=null;
    }
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
   }finally{disk.rename=rename;await faultStore.destroy();}
   await fetch(input.url+'/__fixture/revoke',{method:'POST'});
  await client.synchronize();
  assert.equal((await client.leaseIndicator(job,input.asset)).value,'Unavailable');
  assert(!JSON.stringify(await store.load()).includes('lease_indicators'));
 }finally{await store.destroy();}
})().catch(e=>{console.error(e);process.exit(1)});
