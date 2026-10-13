// In-memory reference acceptance harness, not an offline cache or automatic
// replay engine. A real field client must additionally implement API-03 storage.
export class ClockReference {
  constructor(base, bearer) { this.base=base; this.bearer=bearer; this.pending=new Map(); }
  async call(path, method, body) {
    const response=await fetch(this.base+path,{method,credentials:'omit',cache:'no-store',redirect:'error',headers:{Authorization:`Bearer ${this.bearer}`,'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body)});
    return {status:response.status,...await response.json()};
  }
  async renew() {
    const result=await this.call('/time-entries/lease','POST',{});
    if (result.status!==200) throw new Error(result.error?.code);
    this.lease=result.data; return this.lease;
  }
  stage(kind, fields, occurredAt) {
    if (!['in','out'].includes(kind) || !this.lease || !occurredAt ||
        !Number.isFinite(Date.parse(occurredAt)) || Date.parse(occurredAt)<Date.parse(this.lease.issued_at) ||
        Date.parse(occurredAt)>Date.parse(this.lease.expires_at) || Date.now()>Date.parse(this.lease.expires_at)) throw new Error('lease_locked');
    const command=Object.freeze({...structuredClone(fields),operation_id:crypto.randomUUID(),occurred_at:occurredAt,lease_id:this.lease.id});
    this.pending.set(command.operation_id,{kind,command,state:'pending'}); return command.operation_id;
  }
  async submit(id) {
    const op=this.pending.get(id);
    if (!op || op.state==='terminal') throw new Error('explicit_repair_required');
    if (op.state==='accepted') return op.result;
    try {
      const result=await this.call(`/time-entries/clock-${op.kind}`,'POST',op.command);
      op.result=result;
      op.state=result.status===200?'accepted':result.status>=500||result.status===429?'retryable':'terminal';
      return result;
    } catch(error) { op.state='unknown'; throw error; }
  }
  async receipt(id) { return this.call(`/time-operations/${id}`,'GET'); }
  withoutLocation(id) {
    const original=this.pending.get(id);
    if (original?.state!=='terminal'||!['invalid_gps','gps_immutable'].includes(original.result.error?.code)) throw new Error('location_repair_not_available');
    const command=structuredClone(original.command);
    delete command.gps; command.operation_id=crypto.randomUUID();
    this.pending.set(command.operation_id,{kind:original.kind,command:Object.freeze(command),state:'pending'});
    return command.operation_id;
  }
}
