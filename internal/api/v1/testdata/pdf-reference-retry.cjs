'use strict';
const assert = require('node:assert/strict');
const { createHash, randomUUID } = require('node:crypto');
const { PDFReference } = require('../reference/pdf-client.cjs');
const [base, originalToken, otherToken, document] = process.argv.slice(2);
(async () => {
 let token=originalToken, epoch, loseBefore=false, loseAfter=false, ambiguous=false;
 const events=[];
 const identity=()=>createHash('sha256').update(token).digest('hex');
 const request=async(method,path,input,binding)=>{
  events.push({method,path,intent:input&&JSON.stringify(input)});
  if(binding&&binding.client!==identity())return {status:401,body:{error:{code:'authentication_changed'}},client:identity(),epoch};
  if(method==='POST'&&loseBefore){loseBefore=false;throw new Error('connection lost before transmission');}
  const response=await fetch(base+path,{method,headers:{Authorization:'Bearer '+token,'Content-Type':'application/json',...((binding?.epoch||epoch)?{'X-FreeFSM-Session-Epoch':binding?.epoch||epoch}:{})},body:input?JSON.stringify(input):undefined});
  const result={status:response.status,body:await response.json(),client:identity(),epoch:response.headers.get('X-FreeFSM-Session-Epoch')};
  if(method==='POST'&&loseAfter){loseAfter=false;throw new Error('response lost after commit');}
  if(ambiguous&&path.startsWith('/pdf-operations/')){ambiguous=false;return {...result,body:{message:'proxy missing route'}};}
  return result;
 };
 const control=async action=>assert.equal((await fetch(base+'/test-control/'+action,{method:'POST',headers:{Authorization:'Bearer '+originalToken}})).status,204);
 const sourcePath=`/invoices/${document}/pdf/source`;
 let reviewed=await request('GET',sourcePath);assert.equal(reviewed.status,200);epoch=reviewed.epoch;assert.ok(epoch);
 const client=()=>new PDFReference(request,{client:identity(),epoch});
 const posts=()=>events.filter(e=>e.method==='POST');
 const lookups=()=>events.filter(e=>e.path.startsWith('/pdf-operations/'));
 let reference=client();
 const entry=reference.stage('invoice',reviewed.body.data,[],{occurredAt:'2026-01-02T03:04:05Z'});
 const originalIntent=JSON.stringify(entry.intent);
 assert.throws(()=>{entry.kind='estimate';},TypeError);
 assert.throws(()=>{entry.intent.occurred_at='2026-02-01T00:00:00Z';},TypeError);
 await assert.rejects(client().retryUnreceived(entry,{confirm:true}),/review_required/);
 await reference.submit(entry);assert.equal(entry.state,'unknown');assert.equal(posts().length,1);
 await reference.submit(entry);assert.equal(entry.state,'retryable');assert.equal(posts().length,1);assert.equal(lookups().length,1);
 await assert.rejects(reference.submit(entry),/review_required/);
 await assert.rejects(reference.retryUnreceived(entry),/review_required/);
 assert.equal(posts().length,1,'ordinary404 or absent confirmation automatically replayed');
 await control('repair');
 await reference.retryUnreceived(entry,{confirm:true});assert.equal(entry.state,'accepted');assert.equal(posts().length,2);assert.equal(lookups().length,2);
 assert.deepEqual(events.slice(-3).map(event=>event.method+' '+event.path),['GET /pdf-operations/'+entry.intent.operation_id,'GET '+sourcePath,`POST /invoices/${document}/pdf/generate`]);
 assert.equal(events.filter(event=>event.path===sourcePath).length,3);
 console.log('Ordinary transient recovery: POST attempts=2, receipt lookups=2, source reviews=3; lookup/source recheck precede explicit unchanged retry.');
 assert.equal(JSON.stringify(entry.intent),originalIntent);assert.equal(posts()[0].intent,posts()[1].intent);
 assert.equal(entry.intent.occurred_at,'2026-01-02T03:04:05Z');
 await assert.rejects(reference.submit(entry),/review_required/);

 reference=client();const arriving=reference.stage('invoice',reviewed.body.data);loseBefore=true;
 await reference.submit(arriving);await reference.submit(arriving);assert.equal(arriving.state,'retryable');
 assert.equal((await request('POST',`/invoices/${document}/pdf/generate`,arriving.intent)).status,200);
 const beforeArriving=posts().length;await reference.retryUnreceived(arriving,{confirm:true});
 assert.equal(arriving.state,'accepted');assert.equal(posts().length,beforeArriving,'receipt appeared before explicit retry but client posted again');

 reference=client();const precommit=reference.stage('invoice',reviewed.body.data);loseBefore=true;
 await reference.submit(precommit);assert.equal(precommit.state,'unknown');await reference.submit(precommit);assert.equal(precommit.state,'retryable');
 const precommitID=precommit.intent.operation_id;await reference.retryUnreceived(precommit,{confirm:true});assert.equal(precommit.state,'accepted');assert.equal(precommit.intent.operation_id,precommitID);
 reference=client();const committed=reference.stage('invoice',reviewed.body.data);loseAfter=true;
 await reference.submit(committed);assert.equal(committed.state,'unknown');const beforeLookup=posts().length;
 await reference.submit(committed);assert.equal(committed.state,'accepted');assert.equal(posts().length,beforeLookup,'committed receipt was resent');

 await control('fault');reference=client();const stale=reference.stage('invoice',reviewed.body.data);
 await reference.submit(stale);
 const doc=(await request('GET',`/invoices/${document}`)).body.data;
 assert.equal((await request('PATCH',`/invoices/${document}`,{operation_id:randomUUID(),job_id:doc.job_id,revision:doc.revision,notes:'Changed before explicit retry'})).status,200);
 await reference.submit(stale);assert.equal(stale.state,'needs_review');await assert.rejects(reference.retryUnreceived(stale,{confirm:true}),/review_required/);
 reviewed=await request('GET',sourcePath);

 for(const condition of ['ambiguous','client','restore','auth','epoch']){
  if(condition==='auth')token=otherToken;
  reference=client();const pending=reference.stage('invoice',reviewed.body.data);await reference.submit(pending);assert.equal(pending.state,'unknown');const before=posts().length;
  if(condition==='ambiguous')ambiguous=true;
  if(condition==='client')token=otherToken;
  if(condition==='restore')reference.restoreDetected();
  if(condition==='auth')await control('revoke-other');
  if(condition==='epoch')await control('epoch');
  if(condition!=='restore')await reference.submit(pending);
  assert.equal(pending.state,'locked',condition+' must not authorize missing-receipt retry');
  await assert.rejects(reference.retryUnreceived(pending,{confirm:true}),/review_required/);
  assert.equal(posts().length,before,condition+' automatically replayed');token=originalToken;
 }
 console.log('PASS actual HTTP PDF reference: storage503 + ordinary404 -> explicit same-intent retry; lookup counts; pre/postcommit loss; source drift; ambiguous/auth/client/restore/epoch locks');
})().catch(error=>{console.error(error);process.exit(1);});
