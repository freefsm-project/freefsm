'use strict';
const assert=require('node:assert/strict');
const {SubtaskReference}=require('../../../api/v1/reference/subtasks-reference.cjs');
const [base,token,jobID]=process.argv.slice(2);
(async()=>{
  let writes=0, lose=true;
  const call=async(method,path,body)=>{
    const response=await fetch(base+path,{method,headers:{Authorization:`Bearer ${token}`,'Content-Type':'application/json'},body:body?JSON.stringify(body):undefined});
    const result=await response.json();
    if(method==='PATCH'){writes++;if(lose){lose=false;throw new Error('simulated lost response after server commit');}}
    return {status:response.status,...result};
  };
  const initial=await call('GET',`/jobs/${jobID}/subtasks`);assert.equal(initial.status,200);
  const reference=new SubtaskReference(call,()=>true);
  const operation=reference.stage(jobID,initial.data.subtasks[0],true);
  assert.equal(writes,0,'offline staging sent a mutation');
  const immutableIntent=JSON.stringify(reference.pending.get(operation).body);
  assert.equal((await reference.submit(operation)).status,0);
  assert.equal(reference.pending.get(operation).state,'unknown');
  const receipt=await reference.reconcile(operation);
  assert.equal(receipt.status,200);assert.equal(receipt.data.subtask_id,initial.data.subtasks[0].id);
  assert.equal(reference.pending.get(operation).state,'accepted');assert.equal(writes,1);
  assert.equal(JSON.stringify(reference.pending.get(operation).body),immutableIntent);
  const replay=await call('PATCH',`/jobs/${jobID}/subtasks/${initial.data.subtasks[0].id}`,reference.pending.get(operation).body);
  assert.deepEqual(replay.data,receipt.data);
  const changed={...reference.pending.get(operation).body,completed:false};
  assert.equal((await call('PATCH',`/jobs/${jobID}/subtasks/${initial.data.subtasks[0].id}`,changed)).status,409);
  const current=await call('GET',`/jobs/${jobID}/subtasks`);
  assert.equal(current.data.history.filter(e=>e.action==='complete').length,1);
  const denied=new SubtaskReference(call,()=>false);assert.throws(()=>denied.stage(jobID,initial.data.subtasks[0],false),/staging_locked/);
  console.log(JSON.stringify({result:'passed',stable_id:receipt.data.subtask_id,operation,one_completion_event:true,lost_response_reconciled:true}));
})().catch(error=>{console.error(error);process.exitCode=1;});
