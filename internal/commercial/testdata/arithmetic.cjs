const assert = require('node:assert/strict');
const test = require('node:test');
const {calculate,format,signature} = require('../../../cmd/freefsm/static/js/commercial-arithmetic.js');
test('staged half-away cents and individual line tax',()=>{
 const got=calculate({tax_rate:'10',lines:[{quantity:'0.335',unit_price:'3.00',taxable:true,discount:{kind:'percentage',value:'50'},surcharge:{kind:'percentage',value:'10'}},{quantity:'1',unit_price:'0.05',taxable:true}]});
 assert.equal(got.lines[0].base_cents,101n);
 assert.equal(got.lines[0].adjusted_cents,56n);
 assert.equal(format(got.total_cents),'0.68');
 assert.equal(got.tax_cents,7n);
});
test('combined line and document adjustments, signed and mixed taxable lines',()=>{
 const got=calculate({tax_rate:'8.25',discount:{kind:'fixed',value:'1.00'},surcharge:{kind:'percentage',value:'10'},lines:[{quantity:'1.5',unit_price:'10.01',taxable:true,discount:{kind:'percentage',value:'10'},surcharge:{kind:'fixed',value:'0.05'}},{quantity:'1',unit_price:'5.00'},{quantity:'1',unit_price:'-0.50',taxable:true}]});
 assert.deepEqual(got.lines.map(line=>line.adjusted_cents),[1413n,520n,-50n]);
 assert.equal(got.pretax_cents,1883n);assert.equal(got.tax_cents,113n);assert.equal(format(got.total_cents),'19.96');
});
test('allocation stable ties, exhaustion and no targets',()=>{
 const got=calculate({tax_rate:'50',discount:{kind:'fixed',value:'0.01'},surcharge:{kind:'fixed',value:'0.01'},lines:[{quantity:'1',unit_price:'0.01',taxable:true},{quantity:'1',unit_price:'0.01',taxable:true},{quantity:'1',unit_price:'0.01'}]});
 assert.deepEqual(got.lines.map(line=>line.adjusted_cents),[0n,2n,1n]);assert.equal(got.total_cents,4n);
 assert.throws(()=>calculate({tax_rate:'0',surcharge:{kind:'fixed',value:'0.01'},lines:[]}));
 assert.equal(calculate({tax_rate:'0',discount:{kind:'fixed',value:'1000.00'},surcharge:{kind:'percentage',value:'10'},lines:[{quantity:'1',unit_price:'1.00'}]}).total_cents,0n);
});
test('exact cents beyond IEEE-754 and invalid quantities',()=>{
 assert.equal(format(calculate({tax_rate:'0',lines:[{quantity:'1',unit_price:'90071992547409.93'}]}).total_cents),'90071992547409.93');
 for(const quantity of ['0','-1','NaN','Infinity'])assert.throws(()=>calculate({tax_rate:'0',lines:[{quantity,unit_price:'1.00'}]}));
 assert.throws(()=>calculate({tax_rate:'0',lines:[{quantity:'0.0001',unit_price:'92233720368547758.08'}]}));
});
test('cosmetic decimal formatting does not turn metadata into a monetary edit',()=>{
 assert.deepEqual(signature({tax_rate:'10.0',lines:[{quantity:'1.00',unit_price:'10.00',taxable:true}]}),signature({tax_rate:'10',lines:[{quantity:'1',unit_price:'10',taxable:true}]}));
});
