// Bounded Item reference harness, not a general offline client. Platform storage,
// leases and document operation receipts belong to their API protocol slices.
// Only explicit allowlisted fields enter this cache or a captured commercial line.
class ItemReferenceHarness {
  #cache = new Map();
  #lookup;
  constructor(lookup) { this.#lookup = lookup; }
  async refresh(id) {
    const response = await this.#lookup(id);
    if (!response.ok) {
      if ([401, 403, 404].includes(response.status)) this.#cache.clear();
      throw new Error('selection_requires_review');
    }
    const { data: item } = await response.json();
    const reference = { id: item.id, name: item.name, type: item.type, description: item.description };
    if (typeof item.unit_price === 'string') {
      Object.assign(reference, { unit_price: item.unit_price, taxable: item.taxable, tax_rate: item.tax_rate, sku: item.sku });
    }
    this.#cache.set(id, Object.freeze(reference));
    return structuredClone(reference);
  }
  stageSelection(id, quantity = 1) {
    const item = this.#cache.get(id);
    if (!item || item.unit_price === undefined || !(quantity > 0) || !Number.isFinite(quantity)) throw new Error('selection_unavailable');
    return { item_id: item.id, title: item.name, description: item.description, unit_price: Number(item.unit_price), quantity, taxable: item.taxable, tax_rate: item.tax_rate };
  }
  // This is a preview check only. The commercial writer rechecks at commit.
  // Return the user's capture, never the newly fetched catalog price.
  async reviewSelection(capture) {
    await this.refresh(capture.item_id);
    return structuredClone(capture);
  }
  logout() { this.#cache.clear(); }
}
module.exports = { ItemReferenceHarness };
