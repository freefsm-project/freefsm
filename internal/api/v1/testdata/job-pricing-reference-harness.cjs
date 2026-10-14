// Read-only captured Job pricing for supported field workflows. Job planning
// writes remain browser-only. Item selection uses ItemReferenceHarness for
// authorized commercial document workflows, never an implicit Job edit.
class JobPricingReferenceHarness {
  #lookup; #cache = new Map();
  constructor(lookup) { this.#lookup = lookup; }
  async refresh(id) {
    const response = await this.#lookup(id);
    if (!response.ok) {
      if ([401,403,404].includes(response.status)) this.#cache.clear();
      throw new Error('job_pricing_unavailable');
    }
    const {data:job} = await response.json();
    if (!Array.isArray(job.line_items)) { this.#cache.delete(id); return null; }
    const lines = job.line_items.map(line => ({snapshot_id:line.snapshot_id,item_id:line.item_id,title:line.title,description:line.description,unit_price:line.unit_price,quantity:line.quantity,taxable:line.taxable,tax_rate:line.tax_rate,discount:line.discount,surcharge:line.surcharge}));
    const pricing = {id:job.id,billing_type:job.billing_type,line_items:lines};
    this.#cache.set(id,structuredClone(pricing));
    return pricing;
  }
  current(id) { return this.#cache.has(id) ? structuredClone(this.#cache.get(id)) : null; }
  logout() { this.#cache.clear(); }
}
module.exports = { JobPricingReferenceHarness };
