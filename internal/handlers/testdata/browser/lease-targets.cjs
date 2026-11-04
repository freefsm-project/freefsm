const assert = require('node:assert/strict');
module.exports = async function assertTargets(page, selector, coarse) {
  assert.equal(await page.evaluate(() => matchMedia('(any-pointer: coarse)').matches), coarse);
  const targets = await page.locator(selector).evaluateAll(nodes => nodes.map(node => {
    const r = node.getBoundingClientRect();
    return { text: node.textContent.trim(), width: r.width, height: r.height };
  }));
  assert.ok(targets.length > 0, 'production navigation targets exist');
  for (const target of targets) {
    assert.ok(target.width >= (coarse ? 44 : 24) && target.height >= (coarse ? 44 : 24), JSON.stringify({ coarse, ...target }));
  }
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, '320px page must reflow');
  console.log(JSON.stringify({ coarse, measured: targets.length, minimum: coarse ? 44 : 24 }));
};
