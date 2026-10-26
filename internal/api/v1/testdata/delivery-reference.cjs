const assert=require('node:assert/strict');
const {DeliveryReference}=require('../reference/delivery-client.cjs');
(async()=>{
 const [base,token,id,version]=process.argv.slice(2);const path=`/estimates/${id}/deliveries`;
 const context={client:'ticket56',epoch:''};let lose=false,preflightLoss=false;let posts=0;
 const request=async(method,path,input)=>{
  if(path.endsWith('/deliveries')) {posts++;if(preflightLoss){preflightLoss=false;throw Error('unreceived');}}
  const response=await fetch(base+path,{method,headers:{Authorization:'Bearer '+token,'Content-Type':'application/json'},...(input?{body:JSON.stringify(input)}:{})});
  const result={status:response.status,body:await response.json(),client:context.client,epoch:response.headers.get('X-FreeFSM-Session-Epoch')};
  if(lose&&path.endsWith('/deliveries')){lose=false;assert.equal(response.status,200);throw Error('lost accepted response');}return result;
 };
 const message={pdf_version_id:Number(version),confirm_stale_pdf:false,to:['reference@example.test','second@example.test'],cc:[],bcc:[],subject:'Reference reviewed',text_body:'Frozen',html_body:'',extra_file_ids:[]};
 const reviewed=await request('POST',path+'/review',message);assert.equal(reviewed.status,200);context.epoch=reviewed.epoch;
 const client=new DeliveryReference(request,context);const entry=client.stage('estimate',reviewed.body.data,message);
 message.to[0]='changed-after-stage@example.test';assert.equal(entry.intent.to[0],'reference@example.test');
 lose=true;await client.submit(entry);assert.equal(entry.state,'unknown');assert.equal(posts,1);
 await client.submit(entry);assert.equal(entry.state,'accepted');assert.equal(posts,1);assert.equal(entry.receipt.delivery_ids.length,2);
 const retry=client.stage('estimate',reviewed.body.data,{...message,to:['reference@example.test','second@example.test']});
 preflightLoss=true;await client.submit(retry);await client.reconcile(retry);assert.equal(retry.state,'retryable');assert.equal(posts,2);
 await assert.rejects(client.retryUnreceived(retry),/review_required/);
 await client.retryUnreceived(retry,{confirm:true});assert.equal(retry.state,'accepted');assert.equal(posts,3);
 assert.notDeepEqual(entry.receipt.delivery_ids,retry.receipt.delivery_ids);
 client.restoreDetected();await assert.rejects(client.submit(entry),/review_required/);
 console.log('PASS real HTTP Delivery reference: frozen intent, lost acceptance receipt identities, ordinary absence/current review, explicit unchanged retry, restore lock');
})().catch(e=>{console.error(e);process.exit(1);});
