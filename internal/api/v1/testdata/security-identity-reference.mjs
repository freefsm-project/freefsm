import assert from 'node:assert/strict';
import {ClockReference} from '../reference/clock-reference.mjs';
const [base,token]=process.argv.slice(2);
const realFetch=globalThis.fetch;
let losePost=true,loseReceipt=true,posts=0,original;
globalThis.fetch=async (url,options)=>{
  const response=await realFetch(url,options);
  if (String(url).endsWith('/time-entries/lease') && options.method==='POST') {
    posts++;
    original ||= (await response.clone().json()).data;
    if (losePost) {losePost=false;throw new Error('lost committed issuance response');}
  }
  if (String(url).includes('/clock-lease-operations/') && loseReceipt) {
    loseReceipt=false;throw new Error('lost receipt response');
  }
  return response;
};
const client=new ClockReference(base,token);
await assert.rejects(client.renew(),/lost committed/);
const operation=client.leaseOperation.id;
await assert.rejects(client.renew(),/lost receipt/);
assert.equal(client.leaseOperation.id,operation);
assert.deepEqual(await client.renew(),original);
assert.equal(posts,1,'receipt recovery must not submit a fresh issuance');
assert.equal(client.lease.operation_id,operation);
const renewed=await client.renew();
assert.notEqual(renewed.operation_id,operation,'explicit subsequent renewal is a new intent');
assert.notEqual(renewed.id,original.id);
console.log('PASS lease POST loss + receipt GET loss; exact issuance reconciled without resubmission; explicit next renewal has new identity');
