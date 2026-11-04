// Executable protocol reference, not a production offline cache or client.
const assert=require('node:assert/strict');
const {randomUUID}=require('node:crypto');
const fs=require('node:fs');
(async()=>{
 const [base,token,job,asset,user,date,pendingFile]=process.argv.slice(2);
 const path=`/jobs/${job}/assets/${asset}/service-entries`;
 const call=async(method,path,body,status=200)=>{
  const response=await fetch(base+path,{method,headers:{Authorization:`Bearer ${token}`,'Content-Type':'application/json'},body:body?JSON.stringify(body):undefined});
  const json=await response.json();assert.equal(response.status,status,JSON.stringify(json));return json.data;
 };
 const relationship=await call('GET',path);
 let pending={operation_id:randomUUID(),link_id:relationship.link_id,service_date:date,summary:'Original reference service',responsible_user_ids:[Number(user)]};
 // Disposable reference persistence only, not a product encrypted cache.
 fs.writeFileSync(pendingFile,JSON.stringify(pending),{mode:0o600});
 pending=JSON.parse(fs.readFileSync(pendingFile,'utf8'));
 // Lost response is reconciled by the original identity before retry.
 await call('POST',path,pending);
 const receipt=await call('GET',`/service-entry-operations/${pending.operation_id}`);
 assert.equal(receipt.code,'accepted');
 assert.deepEqual(await call('POST',path,pending),receipt);
 let rows=(await call('GET',path)).entries;assert.equal(rows.length,1);
 const original=rows[0];
 const invalidCreate={...pending,operation_id:randomUUID(),revision:original.revision};
 await call('POST',path,invalidCreate,409);
 assert.equal((await call('GET',`/service-entry-operations/${invalidCreate.operation_id}`)).code,'service_entry_conflict');
 const revisionPath=`${path}/${receipt.entry_id}`;
 assert.ok(Array.isArray(original.responsible_user_ids),'fresh editable read supplies responsible identities');
 const revision={operation_id:randomUUID(),link_id:relationship.link_id,service_date:original.service_date,responsible_user_ids:original.responsible_user_ids,revision:original.revision,summary:'Reviewed revision'};
 await call('POST',revisionPath,revision);
 const stale={...revision,operation_id:randomUUID(),summary:'Stale pending text'};
 await call('POST',revisionPath,stale,409);
 await call('POST',revisionPath,stale,409);
 const rejection=await call('GET',`/service-entry-operations/${stale.operation_id}`);
 assert.equal(rejection.code,'service_entry_conflict');
 rows=(await call('GET',path)).entries;assert.equal(rows[0].summary,'Reviewed revision');
 // Explicitly reviewed new intent, preserving exact original target. Never
 // mutate a terminal operation or silently force/merge the stale text.
 const repaired={...stale,operation_id:randomUUID(),revision:rows[0].revision,summary:'Explicitly reviewed repair'};
 await call('POST',revisionPath,repaired);
 await call('POST',path,{...pending,summary:'Changed create intent'},409);
  assert.equal((await call('GET',path)).entries[0].summary,'Explicitly reviewed repair');
  const history=await call('GET',revisionPath+'/revisions');
  assert.equal(history.length,3);
  assert.deepEqual(Object.keys(history[0]).sort(),['accepted_at','actor','responsible_names','revision','service_date','summary']);
 console.log('OFF-13 create-once/lost-response/immutable receipt/stale revision/reviewed new-ID repair PASS');
})().catch(e=>{console.error(e);process.exitCode=1});
