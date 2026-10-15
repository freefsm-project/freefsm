'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const {join}=require('node:path');
const {tmpdir}=require('node:os');
const {randomUUID}=require('node:crypto');
const {OfflineNotes,ProtectedStore}=require('../reference/offline-notes.cjs');
(async()=>{
 const {url,token}=JSON.parse(fs.readFileSync(0,'utf8'));
 const directory=fs.mkdtempSync(join(tmpdir(),'offline-http-')),store=new ProtectedStore(directory,randomUUID());
 let lose=false,loseSync=false,posts=0;
 const transport=async(path,method,body,auth)=>{
  const headers={Authorization:'Bearer '+auth.token};if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;
  if(auth.operation)headers['Operation-ID']=auth.operation;
  if(body!==undefined)headers['Content-Type']='application/json';
  if(loseSync&&path.startsWith('/sync/jobs')){headers['X-Offline-Lose-Response']='yes';loseSync=false;}
  if(method==='POST'&&path.endsWith('/field-notes')){posts++;if(lose){headers['X-Offline-Lose-Response']='yes';lose=false;}}
  const r=await fetch(url+path,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
  return {status:r.status,...await r.json()};
 };
 const fixture=async(name)=>{const r=await fetch(url+'/__fixture/'+name,{method:'POST'});assert(r.ok,'fixture '+name);return r.status===204?null:r.json();};
 let client=new OfflineNotes(store,transport);
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
  await fixture('restore');await fixture('unassign-hidden');await assert.rejects(client.reconcile(pending),/locked_review/);
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
  await assert.rejects(client.submit(pending),/reconciliation_or_review_required/);
  assert.equal((await client.reconcile(op)).state,'accepted');assert.equal(posts,4);
  await client.readNotes(job);await client.acknowledgeUncertain(pending);
  const newID=await client.repair(pending,'reviewed after restore',(await client.view()).notes[job].notes_revision);
  assert.notEqual(newID,pending);assert.equal(posts,4);await client.discard(newID);
  const privateID=await client.stage(job,'must purge on disable');
  await fixture('disable');await assert.rejects(client.submit(privateID),/authorization_purged/);
  await assert.rejects(client.view(),/locked_review/);
  const stored=await store.load();assert.equal(stored.token,null);assert.deepEqual(stored.cache,{});assert(!JSON.stringify(stored).includes('must purge on disable'));
  assert.equal(posts,4);
  await fixture('enable');await client.authenticate(fresh.token);await client.synchronize();await client.readNotes(job);
  const revoked=await client.stage(job,'ordinary revocation must purge');await fixture('revoke-all');
  await assert.rejects(client.reconcile(revoked),/authorization_purged/);
  assert(!JSON.stringify(await store.load()).includes('ordinary revocation must purge'));
  assert.equal(posts,4);
  console.log('PASS real Secret Service + production HTTP/schema: encrypted restart, committed loss, safe receipt after scope loss, restore locked auth+sync, missing receipt uncertainty, explicit new-ID review, disabled purge, no automatic replay');
 }finally{await client.logout();fs.rmSync(directory,{recursive:true,force:true});}
})().catch(e=>{console.error(e.message);process.exitCode=1;});
