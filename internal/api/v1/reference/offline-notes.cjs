#!/usr/bin/env node
'use strict';
const {OfflineNotes,ProtectedStore}=require('./offline-notes-client.cjs');
const {OfflineFiles}=require('./offline-files-client.cjs');
const {OfflineClocks}=require('./offline-clocks-client.cjs');
module.exports={OfflineNotes,OfflineFiles,ProtectedStore};

// JSON command on stdin avoids exposing bearer credentials in argv/history.
// The caller explicitly chooses every action. No timers or background replay.
if(require.main===module){
 (async()=>{
  const fs=require('node:fs');
  const command=JSON.parse(fs.readFileSync(0,'utf8'));
  const {directory,store_id,server,action}=command;
  const url=new URL(server);
  if(url.username||url.password||url.search||url.hash||url.pathname!=='/api/v1'||(url.protocol!=='https:'&&!(url.protocol==='http:'&&['127.0.0.1','[::1]'].includes(url.hostname))))throw Error('trusted_api_origin_required');
  const store=new ProtectedStore(directory,store_id);
  const transport=async(path,method,body,auth)=>{
   const headers={Authorization:'Bearer '+auth.token};
   if(auth.epoch)headers['X-FreeFSM-Session-Epoch']=auth.epoch;
   if(auth.operation)headers['Operation-ID']=auth.operation;
    if(body!==undefined)headers['Content-Type']=Buffer.isBuffer(body)?'application/octet-stream':'application/json';
    const response=await fetch(server+path,{method,headers,body:body===undefined?undefined:Buffer.isBuffer(body)?body:JSON.stringify(body),redirect:'error',signal:AbortSignal.timeout(30000)});
   let parsed;try{parsed=await response.json();}catch{
    // Status-based ordinary denials purge even with a malformed response body.
    if([401,403].includes(response.status))parsed={};else throw Error('response_unavailable');
   }
   return {...parsed,status:response.status};
  };
    const workflow=command.workflow||'notes';
    if(!['notes','clocks'].includes(workflow))throw Error('unknown_action');
    const client=workflow==='clocks'?new OfflineClocks(store,transport):new OfflineFiles(store,transport);
  // Destruction must remain possible when the ciphertext is corrupt: it needs
  // no decryption, server binding, authentication or active authorization lease.
  if(action==='logout'){await client.logout();console.log(JSON.stringify({state:'logged_out'}));return;}
   if(action==='initialize'){await client.initialize();await store.exclusive(async()=>{const s=await store.load();if(s.server&&(s.server!==server||(s.workflow||'notes')!==workflow))throw Error('server_binding_mismatch');s.server=server;s.workflow=workflow;await store.save(s);});}
  else{
    const state=await store.load();if(state.server!==server||(state.workflow||'notes')!==workflow)throw Error('server_binding_mismatch');
   let result;
   switch(action){
   case 'authenticate':result=await client.authenticate(command.token);break;
    case 'synchronize':result=await client.synchronize();break;
    case 'synchronize-delta':result=await client.synchronize(true);break;
    case 'read-notes':if(workflow!=='notes')throw Error('unknown_action');result=await client.readNotes(command.job);break;
   case 'view':result=await client.view();break;
    case 'stage-file-publication':if(workflow!=='notes')throw Error('unknown_action');result={operation_id:await client.stageFilePublication(command.job,command.name,Buffer.from(command.bytes_base64,'base64'),command.depends_on,command.parent),state:'pending'};break;
    case 'transfer-file':if(workflow!=='notes')throw Error('unknown_action');result=await client.transferFile(command.operation_id);break;
    case 'stage-file-mutation':if(workflow!=='notes')throw Error('unknown_action');result={operation_id:await client.stageFileMutation(command.job,command.file_id,command.kind,command.name,command.depends_on),state:'pending'};break;
    case 'repair-file':if(workflow!=='notes')throw Error('unknown_action');result={operation_id:await client.repairFile(command.operation_id,command.review,command.depends_on),state:'pending'};break;
    case 'stage-fields':if(workflow!=='notes')throw Error('unknown_action');result={operation_id:await client.stageFields(command.job,command.values,command.depends_on),state:'pending'};break;
    case 'stage':if(workflow!=='notes')throw Error('unknown_action');result={operation_id:await client.stage(command.job,command.text),state:'pending'};break;
    case 'stage-batch-notes':if(workflow!=='notes')throw Error('unknown_action');result={operation_id:await client.stageBatch(command.job,command.text,command.depends_on),state:'pending'};break;
    case 'stage-clock':if(workflow!=='clocks')throw Error('unknown_action');result={operation_id:await client.stageClock(command.clock_action,command.clock,command.depends_on),state:'pending'};break;
    case 'submit-without-location':if(workflow!=='clocks')throw Error('unknown_action');result={operation_id:await client.submitWithoutLocation(command.operation_id,command.depends_on),state:'pending'};break;
    case 'repair-clock-dependencies':if(workflow!=='clocks')throw Error('unknown_action');result={operation_id:await client.repairDependencies(command.operation_id,command.depends_on),state:'pending'};break;
    case 'stage-status':if(workflow!=='notes')throw Error('unknown_action');result={operation_id:await client.stageStatus(command.job,command.status_id,command.status_revision,command.meaning_revision,command.depends_on),state:'pending'};break;
    case 'stage-subtask':if(workflow!=='notes')throw Error('unknown_action');result={operation_id:await client.stageSubtask(command.job,command.subtask_id,command.completed,command.completion_revision,command.depends_on),state:'pending'};break;
   case 'submit-batch':result=await client.submitBatch(command.operation_ids);break;
    case 'submit':if(workflow!=='notes')throw Error('unknown_action');result=await client.submit(command.operation_id);break;
   case 'reconcile':result=await client.reconcile(command.operation_id);break;
   case 'acknowledge-uncertain':await client.acknowledgeUncertain(command.operation_id);result={state:'review_required'};break;
    case 'repair-fields':if(workflow!=='notes')throw Error('unknown_action');result={operation_id:await client.repairFields(command.operation_id,command.values,command.revisions,command.depends_on),state:'pending'};break;
    case 'repair':if(workflow!=='notes')throw Error('unknown_action');result={operation_id:await client.repair(command.operation_id,command.text,command.notes_revision,command.depends_on),state:'pending'};break;
    case 'repair-status':if(workflow!=='notes')throw Error('unknown_action');result={operation_id:await client.repairStatus(command.operation_id,command.status_id,command.status_revision,command.meaning_revision,command.depends_on),state:'pending'};break;
    case 'repair-subtask':if(workflow!=='notes')throw Error('unknown_action');result={operation_id:await client.repairSubtask(command.operation_id,command.completed,command.completion_revision,command.depends_on),state:'pending'};break;
   case 'discard':await client.discard(command.operation_id);result={state:'discarded'};break;
   default:throw Error('unknown_action');
   }
   console.log(JSON.stringify(result));
  }
 })().catch(e=>{
  const safe=new Set(['key_protection_unavailable','protected_store_invalid','protected_directory_required','invalid_store_identity','store_busy','trusted_api_origin_required','server_binding_mismatch','locked_review','authorization_lease_expired','authorization_purged','authentication_required','reauthorization_unavailable','invalid_lease','synchronization_incomplete','inaccessible_target','notes_unavailable','review_required','reconciliation_or_review_required','receipt_mismatch','restore_reconciliation_required','unknown_operation','unknown_action','response_unavailable']);
  safe.add('protected_store_locked');
  safe.add('retry_after_required');
   safe.add('batch_limit');
   safe.add('file_transfer_unavailable');
  console.error(safe.has(e.message)?e.message:'offline_action_unavailable');process.exitCode=1;
 });
}
