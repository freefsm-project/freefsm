'use strict';
// Native executable reference only. Browser localStorage/IndexedDB are not key
// protection. secret-tool speaks the desktop Secret Service protocol.
const fs = require('node:fs/promises');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const { randomBytes, createCipheriv, createDecipheriv } = require('node:crypto');

class ProtectedStore {
 constructor(directory, id) {
  if (!/^[a-zA-Z0-9-]{1,100}$/.test(id)) throw Error('invalid_store_identity');
  this.directory=path.resolve(directory); this.id=id;
 }
 secret(action, input) {
  const args=[action];
  if(action==='store')args.push('--label=FreeFSM offline reference');
  args.push('application','org.freefsm.offline-reference','store',this.id);
  const r=spawnSync('secret-tool',args,{input,timeout:10000,maxBuffer:4096,encoding:'utf8'});
  // Never propagate subprocess output: it may contain secret material.
  if(r.error || r.signal || r.status!==0) throw Error('key_protection_unavailable');
  return (r.stdout||'').trim();
 }
 key(){
  const value=this.secret('lookup');
  if(!/^[a-f0-9]{64}$/.test(value))throw Error('key_protection_unavailable');
  return Buffer.from(value,'hex');
 }
 async checkDirectory(){
  const stat=await fs.lstat(this.directory);
  if(!stat.isDirectory()||stat.isSymbolicLink()||await fs.realpath(this.directory)!==this.directory||stat.uid!==process.getuid()||(stat.mode&0o077))throw Error('protected_directory_required');
 }
 async create(){
  await this.checkDirectory();
  // Existing encrypted data must never acquire a replacement key silently.
  try{await fs.lstat(path.join(this.directory,'cache'));this.key();return;}catch(e){if(e.code!=='ENOENT')throw e;}
  let existing;
  try{existing=this.key();}catch(e){if(e.message!=='key_protection_unavailable')throw e;}
  if(existing){existing.fill(0);return;}
  const key=randomBytes(32);
  try{this.secret('store',key.toString('hex'));const saved=this.key();if(!saved.equals(key))throw Error('key_protection_unavailable');saved.fill(0);}finally{key.fill(0);}
 }
 async save(value){
  await this.checkDirectory();
  const key=this.key(), nonce=randomBytes(12), tmp=path.join(this.directory,`.cache-${randomBytes(16).toString('hex')}`);
  try{
   const c=createCipheriv('aes-256-gcm',key,nonce);c.setAAD(Buffer.from(`FreeFSM offline v1:${this.id}`));
   const ciphertext=Buffer.concat([c.update(JSON.stringify(value),'utf8'),c.final()]);
   const bytes=Buffer.concat([Buffer.from('FFO1'),nonce,c.getAuthTag(),ciphertext]);
   const f=await fs.open(tmp,'wx',0o600);
   try{await f.writeFile(bytes);await f.sync();}finally{await f.close();}
   await fs.rename(tmp,path.join(this.directory,'cache'));
   const d=await fs.open(this.directory,'r');try{await d.sync();}finally{await d.close();}
  }finally{key.fill(0);await fs.rm(tmp,{force:true});}
 }
 async load(){
  await this.checkDirectory();const key=this.key();
  try{
   const file=path.join(this.directory,'cache'), stat=await fs.lstat(file);
   if(!stat.isFile()||stat.isSymbolicLink()||(stat.mode&0o077))throw Error('protected_store_invalid');
   const bytes=await fs.readFile(file);
   if(bytes.length<32||bytes.subarray(0,4).toString()!=='FFO1')throw Error('protected_store_invalid');
   const c=createDecipheriv('aes-256-gcm',key,bytes.subarray(4,16));c.setAAD(Buffer.from(`FreeFSM offline v1:${this.id}`));c.setAuthTag(bytes.subarray(16,32));
   return JSON.parse(Buffer.concat([c.update(bytes.subarray(32)),c.final()]).toString('utf8'));
  }catch(e){if(e.code==='ENOENT')throw e;throw Error('protected_store_invalid');}finally{key.fill(0);}
 }
 async destroyKey(){this.secret('clear');}
 async destroy(){
  // Remove data even if the service is unavailable; report key deletion failure.
  try{await this.destroyKey();}finally{
   await this.checkDirectory();
   for(const name of await fs.readdir(this.directory))if(name==='cache'||/^\.cache-[a-f0-9]{32}$/.test(name))await fs.rm(path.join(this.directory,name),{force:true});
   const d=await fs.open(this.directory,'r');try{await d.sync();}finally{await d.close();}
  }
 }
 async exclusive(fn){
  await this.checkDirectory();
  // Fail closed on concurrent writers. A crashed writer lock needs explicit
  // operator verification/removal, never an unsafe timeout-based takeover.
  const lock=path.join(this.directory,'.writer');
  let f;try{f=await fs.open(lock,'wx',0o600);}catch(e){if(e.code==='EEXIST')throw Error('store_busy');throw e;}
  try{
   await f.sync();const d=await fs.open(this.directory,'r');try{await d.sync();}finally{await d.close();}
   return await fn();
  }finally{await f.close();if(!this.quarantined)await fs.unlink(lock);}
 }
}
module.exports={ProtectedStore};
