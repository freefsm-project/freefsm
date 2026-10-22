'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const {join}=require('node:path');
const {tmpdir}=require('node:os');
const {randomUUID}=require('node:crypto');
const {spawnSync}=require('node:child_process');
const {OfflineNotes,ProtectedStore}=require('../reference/offline-notes.cjs');
(async()=>{
 const input=JSON.parse(fs.readFileSync(0,'utf8')),{url,token}=input;
 const directory=input.directory||fs.mkdtempSync(join(tmpdir(),'offline-http-')),store=new ProtectedStore(directory,input.store_id||randomUUID());
 let lose=false,loseSync=false,loseLease=false,posts=0,leasePosts=0,issuanceLookups=0;
 let syncFault;
 const transport=async(path,method,body,auth)=>{
  const headers={Authorization:'Bearer '+auth.token};if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;
  if(auth.operation)headers['Operation-ID']=auth.operation;
  if(body!==undefined)headers['Content-Type']='application/json';
  if(loseSync&&path.startsWith('/sync/jobs')){headers['X-Offline-Lose-Response']='yes';loseSync=false;}
  if(path.startsWith('/offline-authorization-operations/'))issuanceLookups++;
  if(method==='POST'&&path==='/offline/lease'){leasePosts++;if(loseLease){headers['X-Offline-Lose-Response']='yes';loseLease=false;}}
  if(method==='POST'&&path.endsWith('/field-notes')){posts++;if(lose){headers['X-Offline-Lose-Response']='yes';lose=false;}}
  const r=await fetch(url+path,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
  const result={status:r.status,...await r.json()};
  if(input.denial&&path.startsWith('/offline-authorization-operations/')){
   assert.equal(result.status,200);assert.equal(result.data.operation_id,input.operation);assert.equal(result.data.code,input.denial);
  }
  if(syncFault?.armed&&((input.issuanceFailure&&path==='/offline/lease')||(!input.issuanceFailure&&path.startsWith('/sync/jobs')&&result.data?.complete))){
   assert.equal(result.status,200);
   const locked=await store.load();
   assert.equal(locked.synced,false,'initial synchronization lock must already be durable');
   if(input.issuanceFailure){
    assert.equal(result.data.code,'accepted');assert.equal(result.data.operation_id,locked.issuance.id);
    assert.notEqual(result.data.epoch,locked.issuance.previous.epoch,'accepted issuance must detect restored authority');
   }else{
    assert(!result.data.records.some(record=>record.id===syncFault.job),'completed snapshot must exclude staged target');
    assert.deepEqual(locked.cache,{});assert.deepEqual(locked.notes,{});
   }
   assert.deepEqual(locked.pending[syncFault.pending].intent,syncFault.intent,'fault must occur after the locking write, before final purge persistence');
   syncFault.armed=false;syncFault.observed=true;
   // Fail actual secret-tool key acquisition only after receiving the full snapshot.
   // The directory stays writable, so ordinary exclusive cleanup could unlink .writer.
   process.env.DBUS_SESSION_BUS_ADDRESS='unix:path=/nonexistent-freefsm-sync-secret-service';
  }
  return result;
 };
 const fixture=async(name)=>{const r=await fetch(url+'/__fixture/'+name,{method:'POST'});assert(r.ok,'fixture '+name);return r.status===204?null:r.json();};
  let client=new OfflineNotes(store,transport);
 if(input.syncQuarantine){
  await assert.rejects(client.view(),/store_busy/);
  await assert.rejects(client.authenticate(token),/store_busy/);
  await assert.rejects(client.synchronize(),/store_busy/);
  console.log('PASS restart remains quarantined after key recovery and reassignment');return;
 }
 if(input.syncFailure||input.issuanceFailure){
  const bus=process.env.DBUS_SESSION_BUS_ADDRESS;
  const restoreKeyFacility=()=>{if(bus===undefined)delete process.env.DBUS_SESSION_BUS_ADDRESS;else process.env.DBUS_SESSION_BUS_ADDRESS=bus;};
  try{
   await client.initialize();await client.authenticate(token);await client.synchronize();
   const view=await client.view(),job=Object.keys(view.cache).find(id=>view.cache[id].title==='Offline original target');assert(job);
   await client.readNotes(job);const pending=await client.stage(job,'scope loss must survive failed final sync write');
   const intent=(await client.view()).pending[pending].intent;
   let bearer=token;
   if(input.issuanceFailure){
    await fixture('restore');bearer=(await fixture('token')).token;
   }else{
    await fixture('unassign');
    if(input.restore){await fixture('restore');bearer=(await fixture('token')).token;await client.authenticate(bearer);}
   }
   syncFault={job,pending,intent,armed:true,observed:false};
   await assert.rejects(input.issuanceFailure?client.authenticate(bearer):client.synchronize(),/protected_store_locked/);
   assert(syncFault.observed,'final-write fault was not reached');
   fs.accessSync(directory,fs.constants.W_OK);assert(fs.existsSync(join(directory,'.writer')),'authority-loss failure must retain durable quarantine');
   restoreKeyFacility();await fixture('assign');
   const restarted=spawnSync(process.execPath,[__filename],{input:JSON.stringify({url,token:bearer,directory,store_id:store.id,syncQuarantine:true}),encoding:'utf8',timeout:30000});
   assert.equal(restarted.status,0,restarted.stderr);
   assert.match(restarted.stdout,/PASS restart remains quarantined/);
   console.log('PASS production authority final-write key failure: '+(input.issuanceFailure?'accepted restore issuance':input.restore?'restore synchronization':'ordinary synchronization')+' quarantines restart');
  }finally{restoreKeyFacility();await fixture('assign');await store.destroy();fs.rmSync(directory,{recursive:true,force:true});}
  return;
 }
  if(input.denial){
   await assert.rejects(client.authenticate(token),/authorization_purged/);
   const purged=await store.load();
   for(const key of ['token','lease','authority','contactEpoch'])assert.equal(purged[key],null,key+' must be purged');
   assert.deepEqual(purged.cache,{});assert.deepEqual(purged.notes,{});assert.equal(purged.synced,false);assert.equal(purged.restore,false);
   assert.deepEqual(purged.issuance,{id:input.operation,state:'terminal',code:input.denial});
   for(const pending of Object.values(purged.pending))assert.deepEqual(Object.keys(pending).sort(),pending.receipt?['receipt','state']:['state']);
   assert.equal(purged.pending[input.pending].state,'purged');
   await assert.rejects(client.view(),/locked_review/);
   console.log(JSON.stringify({leasePosts,issuanceLookups}));return;
  }
 if(input.restart){await client.authenticate(token);await client.synchronize();console.log(JSON.stringify({leasePosts,issuanceLookups}));return;}
 try{
  await client.initialize();await client.authenticate(token);await client.synchronize();
  let view=await client.view(),job=Object.keys(view.cache).find(id=>view.cache[id].title==='Offline original target');assert(job);
  const hidden=Object.keys(view.cache).find(id=>id!==job);assert(hidden);
  await client.readNotes(job);const op=await client.stage(job,'accepted original');
  const original=(await client.view()).pending[op];assert.equal(original.state,'pending');
  assert(!fs.readFileSync(join(directory,'cache')).includes('accepted original'));
  // Actual committed socket loss, then restart and resource access loss.
  lose=true;await assert.rejects(client.submit(op));
  client=new OfflineNotes(new ProtectedStore(directory,store.id),transport);
  await assert.rejects(client.submit(op),/reconciliation_or_review_required/);
  await fixture('unassign');const receipt=await client.reconcile(op);
   assert.equal(receipt.state,'accepted');assert(receipt.receipt.effect_id);assert.equal(posts,1);
   await assert.rejects(client.view(),/locked_review/);await client.synchronize();
  await assert.rejects(client.readNotes(job),/inaccessible_target/);
  await fixture('assign');await client.synchronize();await client.readNotes(job);
  const stale=await client.stage(job,'stale offline edit');
  const authorization={token,epoch:(await store.load()).lease.epoch};
  let state=await transport(`/jobs/${job}/field-notes`,'GET',undefined,authorization);
  assert.equal((await transport(`/jobs/${job}/field-notes`,'POST',{operation_id:randomUUID(),notes_revision:state.data.notes_revision,field_notes:'intervening ABA'},authorization)).status,200);
  state=await transport(`/jobs/${job}/field-notes`,'GET',undefined,authorization);
  assert.equal((await transport(`/jobs/${job}/field-notes`,'POST',{operation_id:randomUUID(),notes_revision:state.data.notes_revision,field_notes:'accepted original'},authorization)).status,200);
  assert.equal((await client.submit(stale)).state,'terminal');
  assert.equal((await client.view()).pending[stale].label,'Needs attention');
  await client.readNotes(job);
  const repaired=await client.repair(stale,'reviewed repair',(await client.view()).notes[job].notes_revision);
  assert.notEqual(repaired,stale);await client.discard(repaired);
  assert.equal(posts,4); // original, two explicit ABA writes, terminal stale attempt
  const pending=await client.stage(job,'never automatically replay');
  await client.readNotes(hidden);const inaccessible=await client.stage(hidden,'restore inaccessible pending secret');
  const before=JSON.stringify((await client.view()).pending[pending].intent);
  await fixture('restore');await fixture('unassign-hidden');await assert.rejects(client.authenticate(token),/locked_review/);
  await assert.rejects(client.view(),/locked_review/);
  client=new OfflineNotes(new ProtectedStore(directory,store.id),transport);
  await assert.rejects(client.view(),/locked_review/);assert.equal(posts,4);
  const fresh=await fixture('token');await client.authenticate(fresh.token);
  await assert.rejects(client.view(),/locked_review/);
  loseSync=true;await assert.rejects(client.synchronize());
  client=new OfflineNotes(new ProtectedStore(directory,store.id),transport);
  await assert.rejects(client.view(),/locked_review/);await client.synchronize();
  view=await client.view();assert.equal(view.pending[pending].state,'restore_uncertain');assert.equal(JSON.stringify(view.pending[pending].intent),before);
  assert.equal(view.pending[op].state,'restore_uncertain','a local pre-restore receipt is not proof of surviving restored history');
  assert.equal(view.pending[inaccessible].state,'purged');assert(!JSON.stringify(view).includes('restore inaccessible pending secret'));assert(!view.cache[hidden]);
   const missing=await client.reconcile(pending);assert.equal(missing.state,'restore_uncertain');
   await assert.rejects(client.view(),/locked_review/);await client.synchronize();
  await assert.rejects(client.submit(pending),/reconciliation_or_review_required/);
   assert.equal((await client.reconcile(op)).state,'accepted');assert.equal(posts,4);
   await client.synchronize();
  await client.readNotes(job);await client.acknowledgeUncertain(pending);
  const newID=await client.repair(pending,'reviewed after restore',(await client.view()).notes[job].notes_revision);
  assert.notEqual(newID,pending);assert.equal(posts,4);await client.discard(newID);
  await client.stage(job,'must purge on disable');
  await fixture('disable');await fixture('unassign');
  assert.equal((await client.reconcile(op)).receipt.code,'accepted','safe original receipt survives disablement and scope loss');
  await assert.rejects(client.view(),/locked_review/);
  await assert.rejects(client.stage(job,'disabled staging'),/locked_review/);
  const stored=await store.load();assert.equal(stored.token,null);assert.deepEqual(stored.cache,{});assert(!JSON.stringify(stored).includes('must purge on disable'));
  assert.equal(posts,4);
  await fixture('enable');await fixture('assign');await client.authenticate(fresh.token);await client.synchronize();await client.readNotes(job);
  const revoked=await client.stage(job,'ordinary revocation must purge');await fixture('revoke-all');
  await assert.rejects(client.reconcile(revoked),/authorization_purged/);
  assert(!JSON.stringify(await store.load()).includes('ordinary revocation must purge'));
  assert.equal(posts,4);
  const scopeToken=await fixture('token');await client.authenticate(scopeToken.token);await client.synchronize();await client.readNotes(job);
  const denied=await client.stage(job,'scope denied first secret'),sibling=await client.stage(job,'scope denied sibling secret');
  await fixture('unassign');assert.equal((await client.submit(denied)).state,'purged');
  client=new OfflineNotes(new ProtectedStore(directory,store.id),transport);
  view=await client.view();assert.equal(view.pending[sibling].state,'purged','denial must purge every intent on the inaccessible target');
  assert(!JSON.stringify(view).includes('scope denied'));assert(!view.cache[job]);assert(!view.notes[job]);
  await fixture('assign');await client.synchronize();await client.readNotes(job);
  const interrupted=await client.stage(job,'restore issuance interrupted secret');
  const interruptedIntent=JSON.stringify((await client.view()).pending[interrupted].intent);
  await fixture('restore');const restored=await fixture('token');
  loseLease=true;await assert.rejects(client.authenticate(restored.token));
  await assert.rejects(client.view(),/locked_review/);
  const restart=spawnSync(process.execPath,[__filename],{input:JSON.stringify({url,token:restored.token,directory,store_id:store.id,restart:true}),encoding:'utf8',timeout:30000});
  assert.equal(restart.status,0,restart.stderr);
  assert.deepEqual(JSON.parse(restart.stdout),{leasePosts:0,issuanceLookups:1},'restart must recover committed issuance receipt, not issue another lease');
  client=new OfflineNotes(new ProtectedStore(directory,store.id),transport);
  view=await client.view();assert.equal(view.pending[interrupted].state,'restore_uncertain','lost issuance response must not erase prior epoch provenance');
  assert.equal(JSON.stringify(view.pending[interrupted].intent),interruptedIntent);
   assert.equal((await client.reconcile(interrupted)).state,'restore_uncertain');
   await assert.rejects(client.view(),/locked_review/);await client.synchronize();
  await assert.rejects(client.submit(interrupted),/reconciliation_or_review_required/);
  await client.readNotes(job);await assert.rejects(client.repair(interrupted,'unreviewed retry',(await client.view()).notes[job].notes_revision),/restore_reconciliation_required/);
  for(const interruptedAuth of [false,true]){
   const ordinary=await client.stage(job,'ordinary fresh auth secret');
   await fixture('revoke-all');const replacement=await fixture('token');
   if(interruptedAuth){
    loseLease=true;await assert.rejects(client.authenticate(replacement.token));
    const restarted=spawnSync(process.execPath,[__filename],{input:JSON.stringify({url,token:replacement.token,directory,store_id:store.id,restart:true}),encoding:'utf8',timeout:30000});
    assert.equal(restarted.status,0,restarted.stderr);assert.deepEqual(JSON.parse(restarted.stdout),{leasePosts:0,issuanceLookups:1});
    client=new OfflineNotes(new ProtectedStore(directory,store.id),transport);
   }else{await client.authenticate(replacement.token);await client.synchronize();}
   view=await client.view();assert.equal(view.pending[ordinary].state,'purged','same-epoch fresh authentication must purge, not manufacture restore');
   assert(!JSON.stringify(view).includes('ordinary fresh auth secret'));
   assert(!JSON.stringify(await store.load()).includes('ordinary fresh auth secret'));
   await client.readNotes(job);
  }
   for(const [deny,restore,code]of [['disable','enable','offline_disabled'],['remove-job-view','restore-job-view','forbidden']]){
    const bearer=(await store.load()).token;
    const pending=await client.stage(job,'lost denial must never revive '+code);
    await fixture(deny);loseLease=true;
    await assert.rejects(client.authenticate(bearer),/fetch failed/,'committed denial response must actually be lost');
    const unknown=await store.load();assert.equal(unknown.issuance.state,'unknown');assert(unknown.pending[pending].intent);
    const operation=unknown.issuance.id;
    const restarted=spawnSync(process.execPath,[__filename],{input:JSON.stringify({url,token:bearer,directory,store_id:store.id,denial:code,operation,pending}),encoding:'utf8',timeout:30000});
    assert.equal(restarted.status,0,code+': '+restarted.stderr);
    assert.deepEqual(JSON.parse(restarted.stdout),{leasePosts:0,issuanceLookups:1},'denial recovery must query original receipt without renewal');
    client=new OfflineNotes(new ProtectedStore(directory,store.id),transport);
    assert(!JSON.stringify(await store.load()).includes('lost denial must never revive'));
    await fixture(restore);await client.authenticate(bearer);await client.synchronize();await client.readNotes(job);
    assert.equal((await client.view()).pending[pending].state,'purged');
    assert(!JSON.stringify(await client.view()).includes('lost denial must never revive'));
    await assert.rejects(client.submit(pending),/reconciliation_or_review_required/);
    const original=await transport('/offline-authorization-operations/'+operation,'GET',undefined,{token:bearer});
    assert.equal(original.status,200);assert.equal(original.data.operation_id,operation);assert.equal(original.data.code,code,'re-enablement must not rewrite terminal denial');
   }
   const disabledSubmit=await client.stage(job,'disabled submission secret');
  await fixture('disable');await assert.rejects(client.submit(disabledSubmit),/authorization_purged/);
  assert(!JSON.stringify(await store.load()).includes('disabled submission secret'));
  assert.equal(posts,5,'only the original four submissions and the explicit scope-denied submission occurred');
  await fixture('enable');
  for(const scenario of [{issuanceFailure:true},{syncFailure:true,restore:false},{syncFailure:true,restore:true}]){
   const bearer=(await fixture('token')).token;
   const fault=spawnSync(process.execPath,[__filename],{input:JSON.stringify({url,token:bearer,...scenario}),encoding:'utf8',timeout:30000});
   assert.equal(fault.status,0,JSON.stringify(scenario)+' final authority write: '+fault.stderr);
   console.log(fault.stdout.trim());
  }
  console.log('PASS real Secret Service + production HTTP/schema: renewal-first restore lock, multi-intent target purge, disabled receipt reconnect with safe scope-lost receipt, committed issuance loss + fresh-process recovery, ordinary fresh-session purge with/without issuance loss, encrypted lifecycle and no automatic replay');
 }finally{await client.logout();fs.rmSync(directory,{recursive:true,force:true});}
})().catch(e=>{console.error(e.message);process.exitCode=1;});
