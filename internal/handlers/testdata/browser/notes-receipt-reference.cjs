const assert = require('node:assert/strict');
const {randomUUID} = require('node:crypto');
const {JobExecutionReference} = require('../../../api/v1/reference/job-execution.js');
(async()=>{
  const [base,job,token]=process.argv.slice(2);
  let fault;
  const calls=[];
  const call=async(path,method,body)=>{
    calls.push({path,method,body});
    const headers={Authorization:'Bearer '+token,'Content-Type':'application/json'};
    if(fault && method==='POST'){headers['X-Notes-Fault']=fault;fault=undefined;}
    const response=await fetch(base+path,{method,headers,body:body?JSON.stringify(body):undefined});
    const data=await response.json();
    if(response.status>=400){
      assert.equal(data.error.status,response.status);
      for(const key of ['code','message','request_id','details','recovery_disposition'])assert.notEqual(data.error[key],undefined);
      assert.equal(data.error.request_id,response.headers.get('X-Request-ID'));
      if(response.status===503){assert.equal(data.error.recovery_disposition,'retry');assert(data.error.retry_after_seconds>0);assert.equal(data.error.retry_after_seconds,Number(response.headers.get('Retry-After')));}
    }
    return {status:response.status,...data};
  };
  const path=`/jobs/${job}/field-notes`;
  const reference=new JobExecutionReference(call,Date.now()+60000);
  const first=(await call(path,'GET')).data;
  const id=randomUUID();
  reference.stage(job,'field-notes',{notes_revision:first.notes_revision,field_notes:'First reference observation'},id);
  fault='unavailable'; assert.equal((await reference.submit(id)).status,503);
  assert.equal(await reference.reconcile(id),null);
  assert.equal((await reference.submit(id)).status,200);
  const receipt=await reference.reconcile(id);
  assert.equal(receipt.code,'accepted');assert(receipt.effect_id);
  const original=calls.find(c=>c.method==='POST').body;
  assert.equal((await call(path,'POST',{...original,field_notes:'Misused identity'})).error.code,'operation_intent_changed');
  const stale=randomUUID();reference.stage(job,'field-notes',{notes_revision:first.notes_revision,field_notes:'Stale'},stale);
  assert.equal((await reference.submit(stale)).status,409);
  assert.equal((await reference.reconcile(stale)).code,'stale_notes_revision');
  const current=(await call(path,'GET')).data;
  const repaired=randomUUID();reference.repair(stale,job,'field-notes',{notes_revision:current.notes_revision,field_notes:'Reviewed repair'},repaired);
  assert.equal((await reference.submit(repaired)).status,200);
  const history=(await call(path+'/history','GET')).data;
  assert.equal(history.filter(h=>h.content==='First reference observation').length,1);
  assert.equal(history.filter(h=>h.content==='Reviewed repair').length,1);
  const latest=(await call(path,'GET')).data;
  const lost=randomUUID();reference.stage(job,'field-notes',{notes_revision:latest.notes_revision,field_notes:'PRIVATE lost response'},lost);
  fault='lose-and-unassign';await assert.rejects(reference.submit(lost));
  const saved=await reference.reconcile(lost);
  assert.equal(saved.code,'accepted');assert(saved.effect_id);
  assert.deepEqual(Object.keys(saved).sort(),['accepted_at','code','effect_id','operation_id']);
  assert.equal((await call(path,'GET')).status,404);
  assert.equal((await call(path+'/history','GET')).status,404);
  await assert.rejects(reference.submit(lost),/review_required/);
  const unauth=await fetch(base+path);assert.equal(unauth.status,401);
  console.log('PASS success, transient same-ID retry, changed intent, conflict/new-ID repair, history, lost response/scope loss, authentication, actual OpenAPI schemas');
})().catch(error=>{console.error(error);process.exitCode=1;});
