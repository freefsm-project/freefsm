const assert=require('node:assert/strict');
const {SessionReference}=require('../../../../docs/api/session-reference.cjs');
(async()=>{
 const [base,ordinaryToken,restoreToken]=process.argv.slice(2);
 const request=async(token,path,method='GET',epoch='')=>{
  const response=await fetch(base+path,{method,headers:{Authorization:'Bearer '+token,'X-FreeFSM-Session-Epoch':epoch}});
  return {status:response.status,epoch:response.headers.get('X-FreeFSM-Session-Epoch'),body:response.status===204?{}:await response.json()};
 };
 const ordinary=new SessionReference();const start=await request(ordinaryToken,'/me');assert.equal(start.status,200);ordinary.authorized(start.epoch);ordinary.cache=['private'];ordinary.stage({target:1,intent:'pending'});
 assert.equal((await request(ordinaryToken,'/session','DELETE',ordinary.epoch)).status,204);
 ordinary.contact((await request(ordinaryToken,'/me','GET',ordinary.epoch)).body);
 assert.equal(ordinary.state,'signed_out');assert.deepEqual(ordinary.pending,[]);assert.deepEqual(ordinary.cache,[]);
 const restored=new SessionReference();const prior=await request(restoreToken,'/sessions');assert.equal(prior.status,200);restored.authorized(prior.epoch);restored.cache=['private'];
 const retainedID=prior.body.data.find(s=>s.current).id;
 const lostID=prior.body.data.find(s=>s.device==='Lost history reference').id;
 restored.stage({target:retainedID,intent:'uncertain'});restored.stage({target:lostID,intent:'inaccessible'});
 await fetch(base+'/fixture/restore',{method:'POST'});
 const lost=await request(restoreToken,'/me','GET',restored.epoch);assert.equal(lost.status,401);restored.contact(lost.body);
 assert.equal(restored.state,'locked_review');assert.equal(restored.pending.length,2);assert.deepEqual(restored.cache,[]);
 assert.throws(()=>restored.review([1]),/fresh authentication/);
  const startPair=await fetch(base+'/pairings',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({device_name:'Fresh reference',operation_id:crypto.randomUUID()})});
  assert.equal(startPair.status,201);const pairing=(await startPair.json()).data;
  // Test fixture drives the separate hosted browser's login/review/approval.
  // The API reference client never receives or submits an account password.
  const approval=await fetch(base+'/fixture/browser-approve',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({code:pairing.user_code})});assert.equal(approval.status,204);
  const login=await fetch(base+'/pairings/redeem',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({device_code:pairing.device_code,operation_id:crypto.randomUUID()})});
  assert.equal(login.status,201);const token=(await login.json()).data.token;
 const current=await request(token,'/sessions');assert.equal(current.status,200);
 restored.authorized(current.epoch);
 // The demonstration intent targets own session records only. The synchronized
 // own-session projection supplies current authority, not a cached permission.
 const review=restored.review(current.body.data.map(s=>s.id));assert.equal(review.length,1);assert.equal(review[0].target,retainedID,'restore review retargeted pending intent');assert.equal(review[0].requiresExplicitReconciliation,true);
 assert.equal(restored.state,'locked_review');console.log('ordinary purge and restore locked, fresh-auth/current-authority review; no replay');
})().catch(e=>{console.error(e);process.exit(1)});
