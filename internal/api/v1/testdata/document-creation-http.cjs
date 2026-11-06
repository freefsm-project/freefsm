'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const {randomUUID}=require('node:crypto');
const {spawnSync}=require('node:child_process');
const {DocumentCreationClient,ProtectedStore}=require('../reference/document-creation-client.cjs');
(async()=>{
 const input=JSON.parse(fs.readFileSync(0,'utf8')),key=input.key||randomUUID();
 const store=new ProtectedStore(input.directory,key);
 if(input.restart){
  const restarted=new DocumentCreationClient(store,async()=>{throw Error('unexpected_network');});
  if(input.lock)await assert.rejects(restarted.view(),new RegExp(input.lock));
  else assert.deepEqual((await restarted.view()).pending[input.id].intent,input.intent);
  return;
 }
 const restart=(id,intent,lock)=>{
  const result=spawnSync(process.execPath,[__filename],{input:JSON.stringify({...input,key,restart:true,id,intent,lock}),encoding:'utf8',timeout:30000});
  assert.equal(result.status,0,result.stdout+result.stderr);
 };
 let offline=false,lose=false,conflict=false;
 const transport=async(path,method='GET',body,auth)=>{
  if(offline)throw Error('offline');
  const headers={Authorization:'Bearer '+auth.token};
  if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;
  if(auth.operation)headers['Operation-ID']=auth.operation;
  if(body!==undefined)headers['Content-Type']='application/json';
  const r=await fetch(input.url+path,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
  const result={status:r.status,...await r.json()};
  if(path==='/'+input.kind+'s'&&r.status>=400)conflict=true;
  if(lose&&path==='/'+input.kind+'s'){lose=false;throw Error('lost_response');}
  return result;
 };
 let client=new DocumentCreationClient(store,transport);
 try{
  await client.initialize();await client.authenticate(input.token);await client.synchronize();
  const view=await client.view(),job=Object.keys(view.cache).find(j=>String(view.cache[j].document_creation[input.kind].job_id)===input.job),item=Object.keys(view.items)[0];
  if(['grant','assignment','lost_grant'].includes(input.scenario))assert.deepEqual(view.cache[job].document_creation[input.kind==='invoice'?'estimate':'invoice'],{can_create:false});
   assert(!JSON.stringify(view.items).includes('unit_cost'));
   if(['no_catalog','no_catalog_restore','lost_item'].includes(input.scenario)){
    await client.synchronize(true);
    const other=Object.keys(view.cache).find(j=>j!==job);
    const values=await client.prepareCreation(input.kind,job,{title:'Independent free text',line_items:[{title:'Free text',quantity:'2',unit_price:'3.25',taxable:false}]});
    const unrelated=await client.stageCreation(input.kind,other,await client.prepareCreation(input.kind,other,{title:'Unrelated empty document'}));
    const unrelatedIntent=(await client.view()).pending[unrelated].intent;
    const id=await client.stageCreation(input.kind,job,values),original=(await client.view()).pending[id].intent;
    restart(id,original);
    let selected,selectedAccepted,selectedOriginal;
    if(input.scenario==='lost_item'){
     const selectedValues=await client.prepareCreation(input.kind,job,{title:'Private selected Item',line_items:[await client.selectItem(item,'1')]});
     selected=await client.stageCreation(input.kind,job,selectedValues);
     selectedAccepted=await client.stageCreation(input.kind,job,selectedValues);
     selectedOriginal=(await client.view()).pending[selectedAccepted].intent;
     lose=true;await assert.rejects(client.submitCreation(selectedAccepted),/lost_response/);
    }else assert.deepEqual(view.items,{});
    if(input.scenario==='no_catalog_restore'){
     const changed=await fetch(input.url+'/__fixture/change',{method:'POST'}),restored=await changed.json();
     await assert.rejects(client.synchronize(),/locked_review/);restart(id,null,'locked_review');
     await client.authenticate(restored.token);await client.synchronize();
     assert.deepEqual((await client.view()).pending[id].intent,original);
     await assert.rejects(client.submitCreation(id),/reconciliation_or_review_required/);
     await client.reconcile(id);await client.synchronize();await client.acknowledgeUncertain(id);
     const next=await client.repairCreation(id,values);
     assert.equal((await client.submitCreation(next)).receipt.outcome,'accepted');
     assert.equal((await client.view()).pending[unrelated].state,'restore_uncertain');
    }else{
     lose=true;await assert.rejects(client.submitCreation(id),/lost_response/);
     if(selected)assert.equal((await fetch(input.url+'/__fixture/change',{method:'POST'})).status,204);
     await client.synchronize();
     const current=await client.view();assert.deepEqual(current.items,{});
     assert.deepEqual(current.pending[unrelated].intent,unrelatedIntent);
     if(selected){assert(!current.pending[selected].intent);assert(!current.pending[selected].items);await assert.rejects(client.selectItem(item,'1'),/selection_unavailable/);}
     const receipt=(await client.reconcile(id)).receipt;assert.equal(receipt.outcome,'accepted');
     const replay=await transport('/'+input.kind+'s','POST',original,{token:input.token});assert.deepEqual(replay.data,receipt);
     if(selectedAccepted){
      const recovered=(await client.reconcile(selectedAccepted)).receipt;assert.equal(recovered.outcome,'accepted');
      const originalReplay=await transport('/'+input.kind+'s','POST',selectedOriginal,{token:input.token});assert.deepEqual(originalReplay.data,recovered);
     }
     await client.synchronize();await client.synchronize(true);
     assert.equal((await client.submitCreation(unrelated)).receipt.outcome,'accepted');
     const document=await transport('/'+input.kind+'s/'+receipt.id,'GET',undefined,{token:input.token});assert.equal(document.data.totals.total,'6.50');
    }
    console.log('Independent catalog '+input.kind+' '+input.scenario+' PASS');return;
   }
   const line=await client.selectItem(item,'1.5');
  assert.equal(line.unit_price,'12.34');
  const reviewed=await client.prepareCreation(input.kind,job,{title:'ENCRYPTED-CREATION-SECRET',line_items:[line]});
  if(input.scenario==='creation'){
   assert.equal(reviewed.notes,'Company notes');assert.equal(reviewed.terms,'Customer terms');
   assert(reviewed[input.kind==='invoice'?'due_date':'valid_through']);
  }
  offline=true;
  const id=await client.stageCreation(input.kind,job,reviewed);
  const original=(await client.view()).pending[id].intent;
  restart(id,original);
  assert.equal(original.customer_id,reviewed.customer_id);
  assert.match(original.client_id,/^[a-f0-9-]{36}$/);
  assert.equal((await client.view()).pending[id].state,'pending');
  for(const name of fs.readdirSync(input.directory))assert(!fs.readFileSync(input.directory+'/'+name).includes(Buffer.from(reviewed.title)));
  offline=false;
  if(input.scenario!=='creation'){
   const other=Object.keys(view.cache).find(j=>j!==job);
   const unrelated=await client.stageCreation(input.kind,other,await client.prepareCreation(input.kind,other,{title:'Unrelated work'}));
   const unrelatedIntent=(await client.view()).pending[unrelated].intent;
   if(input.scenario==='lost_grant'){lose=true;await assert.rejects(client.submitCreation(id),/lost_response/);}
   const changed=await fetch(input.url+'/__fixture/change',{method:'POST'});
   assert([200,204].includes(changed.status));
   if(input.scenario==='lost_grant'){
    const recovered=await client.reconcile(id);assert.equal(recovered.receipt.outcome,'accepted');
    assert(!JSON.stringify(recovered).includes('ENCRYPTED-CREATION-SECRET'));
    await client.synchronize();
    assert.equal((await client.view()).pending[id].receipt.operation_id,id);
    assert.deepEqual((await client.view()).pending[unrelated].intent,unrelatedIntent);
   }else if(input.scenario==='restore'){
    const restored=await changed.json();
    await assert.rejects(client.synchronize(),/locked_review/);restart(id,null,'locked_review');
    await client.authenticate(restored.token);await client.synchronize();
    assert.equal((await client.view()).pending[id].state,'restore_uncertain');
    assert.deepEqual((await client.view()).pending[id].intent,original);
    await assert.rejects(client.submitCreation(id),/reconciliation_or_review_required/);
    await assert.rejects(client.repairCreation(id,reviewed),/restore_reconciliation_required/);
    assert.equal((await client.reconcile(id)).state,'restore_uncertain');
    await client.synchronize();await client.acknowledgeUncertain(id);
    const repair=await client.repairCreation(id,reviewed);assert.notEqual(repair,id);
    assert.equal((await client.submitCreation(repair)).receipt.outcome,'accepted');
    assert.equal((await client.view()).pending[unrelated].state,'restore_uncertain');
   }else if(input.scenario==='assignment'){
    await client.submitCreation(id);restart(id,null,'locked_review');
    assert.equal((await client.reconcile(id)).receipt.outcome,'not_found');
    await client.synchronize();const current=await client.view();
    assert(!current.cache[job]);assert(!current.pending[id].intent);
    assert.deepEqual(current.pending[unrelated].intent,unrelatedIntent);
    assert.equal((await client.submitCreation(unrelated)).receipt.outcome,'accepted');
   }else if(['revoke','grant'].includes(input.scenario)){
    await assert.rejects(client.submitCreation(id),/authorization_purged/);
    restart(id,null,'locked_review');
    const saved=await store.load();assert(!saved.pending[id].intent);assert(!saved.items);assert.deepEqual(saved.cache,{});
   }else{
    const save=store.save.bind(store);let failed=false;
    store.save=async value=>{if(input.scenario==='quarantine'&&conflict&&!failed){failed=true;throw Error('injected_authority_write_failure');}return save(value);};
    if(input.scenario==='quarantine'){
     await assert.rejects(client.submitCreation(id),/protected_store_locked/);assert(failed);restart(id,null,'store_busy');
    }else{
     const response=await client.submitCreation(id);assert.equal(response.status,input.scenario==='required'?422:409);
     await assert.rejects(client.view(),/locked_review/);restart(id,null,'locked_review');
     const rejected=await client.reconcile(id);assert.equal(rejected.state,'terminal');
     await assert.rejects(client.repairCreation(id,reviewed),/locked_review/);
     await client.synchronize();
     const current=await client.view();assert.deepEqual(current.pending[unrelated].intent,unrelatedIntent);
     if(input.scenario==='binding'){
      assert(!current.pending[id].intent);await assert.rejects(client.repairCreation(id,reviewed),/review_required/);
     }else{
      let repairedValues={...reviewed,line_items:[]};
      if(input.scenario==='required'){
       const definition=current.cache[job].document_creation[input.kind].definitions.find(f=>f.name==='New required choice');
       repairedValues={...reviewed,custom_fields:[{definition_id:definition.id,value:'Reviewed'}]};
      }else assert(!current.pending[id].intent,'Inaccessible Item capture must be withheld');
      const next=await client.repairCreation(id,repairedValues);assert.notEqual(next,id);
      assert.equal((await client.submitCreation(next)).receipt.outcome,'accepted');
     }
     assert.deepEqual((await client.view()).pending[unrelated].intent,unrelatedIntent);
     if(input.scenario!=='required')assert.equal((await client.submitCreation(unrelated)).receipt.outcome,'accepted');
    }
   }
   console.log('Encrypted '+input.kind+' '+input.scenario+' recovery PASS');return;
  }
  assert.equal((await fetch(input.url+'/__fixture/price',{method:'POST'})).status,204);
  await client.synchronize(true);
  assert.equal((await client.view()).items[item].unit_price,'42.00');
  assert.deepEqual((await client.view()).pending[id].intent,original);
  lose=true;await assert.rejects(client.submitCreation(id),/lost_response/);
  assert.equal((await client.view()).pending[id].state,'unknown');
  await assert.rejects(client.submitCreation(id),/reconciliation_or_review_required/);
  const receipt=(await client.reconcile(id)).receipt;
  assert.equal(receipt.outcome,'accepted');assert(receipt.number);
  await client.synchronize();
  const document=await transport('/'+input.kind+'s/'+receipt.id,'GET',undefined,{token:input.token});
  assert.equal(document.status,200);assert.equal(document.data.totals.total,'18.51');
  assert.equal(document.data.manual_category,input.kind+':draft');
  assert.equal(document.data.notes,'Company notes');
  if(input.kind==='invoice'){
   assert.equal(document.data.invoice_date.slice(0,10),original.invoice_date);
   assert.equal(document.data.due_date.slice(0,10),original.due_date);
  }else assert.equal(document.data.valid_through.slice(0,10),original.valid_through);
  assert.equal(document.data.line_items[0].unit_price,'12.34');
  const replay=await transport('/'+input.kind+'s','POST',original,{token:input.token});
  assert.deepEqual(replay.data,receipt);
  await assert.rejects(client.stageCreation(input.kind,job,{...reviewed,title:''}),/review_required/);
  await assert.rejects(client.stageCreation(input.kind,job,{...reviewed,line_items:[{...line,unit_cost:'9.87'}]}),/review_required/);
  await assert.rejects(client.prepareCreation(input.kind,job,{title:'Nonfinite',tax_rate:NaN}),/invalid_document/);
  for(const vector of [
   {name:'empty',lines:[],tax:'0',total:'0.00'},
   {name:'signed half cent',lines:[{title:'positive',quantity:'1',unit_price:'1.00',taxable:false},{title:'signed',quantity:'0.5',unit_price:'-0.01',taxable:false}],tax:'0',total:'0.99'},
   {name:'staged rounding',lines:[{title:'fractional',quantity:'0.335',unit_price:'3.00',taxable:true,discount_adjustment:{kind:'percentage',value:'50'},surcharge_adjustment:{kind:'percentage',value:'10'}},{title:'tax rounding',quantity:'1',unit_price:'0.05',taxable:true}],tax:'10',total:'0.68'},
   {name:'independent adjustments',lines:[{title:'both',quantity:'1',unit_price:'10.00',taxable:true,discount_adjustment:{kind:'fixed',value:'2.00'},surcharge_adjustment:{kind:'percentage',value:'25'}}],tax:'10',adjustments:{discount:{kind:'percentage',value:'50'},surcharge:{kind:'fixed',value:'1.00'}},total:'6.60'},
   {name:'exempt',lines:[{title:'exempt',quantity:'1',unit_price:'1.00',taxable:true}],tax:'100',total:'1.00',exempt:true},
  ]){
   assert.equal((await fetch(input.url+'/__fixture/exempt?enabled='+!!vector.exempt,{method:'POST'})).status,204);
   const values=await client.prepareCreation(input.kind,job,{title:vector.name,line_items:vector.lines,tax_rate:vector.tax,...(vector.adjustments?{adjustments:vector.adjustments}:{}),...(input.kind==='invoice'?{invoice_date:'2024-02-29',due_date:''}:{valid_through:''})});
   assert.equal(values.custom_fields.length,7);
   const operation=await client.stageCreation(input.kind,job,values);
   const accepted=(await client.submitCreation(operation)).receipt;assert.equal(accepted.outcome,'accepted');
   const result=await transport('/'+input.kind+'s/'+accepted.id,'GET',undefined,{token:input.token});
   assert.equal(result.data.totals.total,vector.total);
   assert.equal(result.data.manual_category,input.kind+':draft');
   if(input.kind==='invoice'){assert.equal(result.data.invoice_date.slice(0,10),'2024-02-29');assert.equal(result.data.due_date,null);}
  }
  assert.equal((await fetch(input.url+'/__fixture/exempt?enabled=false',{method:'POST'})).status,204);
  const negative=await client.stageCreation(input.kind,job,await client.prepareCreation(input.kind,job,{title:'Reject negative pretax',line_items:[{title:'negative',quantity:'1',unit_price:'-0.01',taxable:false}]}));
  const rejected=await client.submitCreation(negative);assert.equal(rejected.status,422);assert.equal(rejected.code,'invalid_document');
  await assert.rejects(client.view(),/locked_review/);await client.reconcile(negative);await client.synchronize();
  await client.discard(negative);
  const renewal=await client.stageCreation(input.kind,job,await client.prepareCreation(input.kind,job,{title:'Pending across lease renewal'}));
  const renewalIntent=(await client.view()).pending[renewal].intent;
  client.now=()=>Date.now()+73*3600000;
  await assert.rejects(client.view(),/authorization_lease_expired/);restart(renewal,null,'authorization_lease_expired');
  client.now=()=>Date.now();await client.authenticate(input.token);await client.synchronize();
  assert.deepEqual((await client.view()).pending[renewal].intent,renewalIntent);
  assert.equal((await client.view()).pending[renewal].state,'pending');
  await client.logout();assert(!fs.existsSync(input.directory+'/cache'));assert.throws(()=>store.key(),/key_protection_unavailable/);
  console.log('Encrypted '+input.kind+' creation, offline UUID, restart, price delta, exact total and lost-response original receipt PASS');
 }finally{if(fs.existsSync(input.directory+'/cache'))await store.destroy();}
})().catch(e=>{console.error(e);process.exitCode=1});
