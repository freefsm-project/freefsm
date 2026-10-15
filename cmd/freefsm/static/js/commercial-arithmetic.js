/* COM-03 editor preview. All monetary and quantity arithmetic is BigInt rational;
 * the server independently accepts the reviewed inputs at commit. */
(function (root) {
  'use strict';
  const limit = 9223372036854775807n;
  const invalid = () => { throw new Error('Review quantities, prices, adjustments and totals.'); };
  const decimal = value => {
    const text = String(value);
    if (!/^-?\d+(\.\d+)?$/.test(text)) return invalid();
    const parts = text.split('.');
    return [BigInt(parts.join('')), 10n ** BigInt((parts[1] || '').length)];
  };
  const round = ([n, d]) => {
    const sign = n < 0n ? -1n : 1n;
    n *= sign;
    const result = sign * (n / d + (n % d * 2n >= d ? 1n : 0n));
    if (result > limit || result < -limit - 1n) return invalid();
    return result;
  };
  const moneyText = value => {
    let text = String(value);
    if (/^-?\d+$/.test(text)) text += '.00';
    else if (/^-?\d+\.\d$/.test(text)) text += '0';
    if (/^-?\d+\.\d{2}0+$/.test(text)) text=text.replace(/(\.\d{2})0+$/,'$1');
    if (!/^-?\d+\.\d{2}$/.test(text)) return invalid();
    return text;
  };
  const money = value => { const [n,d] = decimal(moneyText(value)); return round([n*100n,d]); };
  const format = cents => {
    cents = BigInt(cents);
    const sign = cents < 0n ? '-' : '';
    if (cents < 0n) cents = -cents;
    return sign + (cents/100n) + '.' + String(cents%100n).padStart(2,'0');
  };
  const stage = (base, adjustment, discount) => {
    if (!adjustment) return base;
    let [n,d] = decimal(adjustment.value);
    if (n < 0n) return invalid();
    if (adjustment.kind === 'fixed') { n=money(adjustment.value); d=1n; }
    else if (adjustment.kind === 'percentage') { n*=base; d*=100n; }
    else return invalid();
    const result=base*d+(discount?-n:n);
    return discount && result<0n ? 0n : round([result,d]);
  };
  const allocate = (lines, adjustment, discount) => {
    if (!adjustment) return;
    const pool=lines.reduce((sum,line)=>sum+(line.adjusted_cents>0n?line.adjusted_cents:0n),0n);
    if (pool>limit) return invalid();
    const next=stage(pool,adjustment,discount);
    if (pool===0n) { if (next!==0n) invalid(); return; }
    const delta=discount?pool-next:next-pool;
    let left=delta;
    const shares=[];
    lines.forEach((line,index)=>{
      if (line.adjusted_cents<=0n) return;
      const numerator=delta*line.adjusted_cents;
      const cents=numerator/pool;
      shares.push({index,cents,remainder:numerator%pool}); left-=cents;
    });
    shares.sort((a,b)=>a.remainder===b.remainder?a.index-b.index:a.remainder>b.remainder?-1:1);
    shares.forEach((share,index)=>{
      const cents=share.cents+(BigInt(index)<left?1n:0n);
      lines[share.index].adjusted_cents+=discount?-cents:cents;
      lines[share.index][discount?'document_discount_cents':'document_surcharge_cents']=cents;
    });
  };
  const calculate = input => {
    let [rn,rd]=decimal(input.tax_rate || '0');
    if (rn<0n || rn>100n*rd) return invalid();
    if (input.tax_exempt) rn=0n;
    const lines=input.lines.map(line=>{
      const [qn,qd]=decimal(line.quantity);
      if(qn<=0n) return invalid();
      const price=money(line.unit_price);
      if(price<0n && (line.discount||line.surcharge)) throw new Error('Negative-price lines cannot carry adjustments; leave discount and surcharge blank.');
      const base=round([price*qn,qd]);
      const discounted=stage(base,line.discount,true),surcharged=stage(discounted,line.surcharge,false);
      return {base_cents:base,after_discount_cents:discounted,after_surcharge_cents:surcharged,document_discount_cents:0n,document_surcharge_cents:0n,adjusted_cents:surcharged,tax_cents:0n};
    });
    allocate(lines,input.discount,true); allocate(lines,input.surcharge,false);
    let pretax=0n,tax=0n,taxable=0n;
    lines.forEach((line,index)=>{
      if(input.lines[index].taxable) {line.tax_cents=round([line.adjusted_cents*rn,rd*100n]);taxable+=line.adjusted_cents;}
      pretax+=line.adjusted_cents; tax+=line.tax_cents;
    });
    const total=pretax+tax;
    if(pretax<0n||total<0n||pretax>limit||total>limit||tax>limit||tax< -limit-1n||taxable>limit||taxable< -limit-1n) return invalid();
    return {lines,pretax_cents:pretax,tax_cents:tax,total_cents:total};
  };
  const canonical = value => {
    try {let [n,d]=decimal(value),a=n<0n?-n:n,b=d;while(b){const r=a%b;a=b;b=r;}return String(n/a)+'/'+String(d/a);}catch(_){return String(value);}
  };
  const signature = input => {
    const adjustment = a => a ? {kind:a.kind,value:canonical(a.value)} : null;
    return {tax_rate:canonical(input.tax_rate),discount:adjustment(input.discount),surcharge:adjustment(input.surcharge),lines:input.lines.map(line=>({quantity:canonical(line.quantity),unit_price:canonical(line.unit_price),taxable:line.taxable,discount:adjustment(line.discount),surcharge:adjustment(line.surcharge)}))};
  };
  root.freefsmCommercial={calculate,format,moneyText,signature};
  if(typeof module!=='undefined') module.exports=root.freefsmCommercial;
})(typeof window==='undefined'?globalThis:window);
