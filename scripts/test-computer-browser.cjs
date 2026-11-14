// Run inside the computer image with the repository mounted at /src:
// docker run --rm --entrypoint node -v "$PWD:/src:ro" <image> /src/scripts/test-computer-browser.cjs
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {chromium} = require('/opt/browser/node_modules/playwright');
const {run} = require('../packaging/computer/browser.cjs');
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
(async () => {
 const browser = await chromium.launch({headless: true, executablePath: chromium.executablePath(), args: ['--no-sandbox', '--remote-debugging-port=19222']});
 try {
  process.env.OPENVIBELY_CDP_PORT = '19222';
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.route('**/*', route => route.fulfill({contentType: 'text/html', body: '<button onclick="document.querySelector(\'p\').textContent=\'Clicked\'">Click me</button><p>Ready</p>'}));
  const template = fs.readFileSync(path.join(__dirname, '../web/templates/pages/project_computer.templ'), 'utf8');
  const viewerUUID = template.match(/function computerUUID\(\) \{[\s\S]*?\n\s*\}/)[0];
  for (const scheme of ['http', 'https']) {
   await page.goto(`${scheme}://example.test/`);
   assert.equal(await page.evaluate(() => typeof crypto.randomUUID), scheme === 'http' ? 'undefined' : 'function');
   // Exercise the actual viewer's generator in both browser security contexts.
   const ids = await page.evaluate(`(() => { ${viewerUUID}; return Array.from({length: 100}, () => computerUUID()); })()`);
   ids.forEach(id => assert.match(id, uuid));
   assert.equal(new Set(ids).size, ids.length);
   const first = await run({action: 'read'});
   assert.match(first.document, uuid);
   assert.match(first.controls[0].ref, uuid);
   const second = await run({action: 'read'});
   assert.equal(first.snapshot, second.snapshot);
   const clicked = await run({action: 'click', snapshot: first.snapshot, ref: first.controls[0].ref});
   assert.match(clicked.text, /Clicked/);
   assert.notEqual(clicked.snapshot, first.snapshot);
   await assert.rejects(run({action: 'click', snapshot: first.snapshot, ref: first.controls[0].ref}), /Page changed/);
   console.log(`${scheme}: viewer UUIDs, stable references, clicking, and stale-snapshot rejection passed`);
  }
 } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
