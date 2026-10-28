#!/usr/bin/env node
import {spawn} from 'node:child_process';
import {mkdtemp, readFile, rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join, resolve} from 'node:path';
import {fileURLToPath} from 'node:url';
import http from 'node:http';
import XLSX from '../web/assets/file-vendor/xlsx.full.min.js';

const root = resolve(fileURLToPath(new URL('..', import.meta.url)));
const args = process.argv.slice(2);
const option = name => args.includes(name) ? args[args.indexOf(name) + 1] : undefined;
const chrome = option('--chrome') || process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const workerPath = resolve(option('--worker') || join(root, 'web/assets/files/file-spreadsheet-worker.js'));
const protocol = option('--protocol') || 'current';
const runs = Number(option('--runs') || 3);
if (!['current', 'legacy'].includes(protocol)) throw new Error('--protocol must be current or legacy');

console.log('Generating dense fixture: 8 worksheets × 25,000 rows × 20 columns');
const workbook = XLSX.utils.book_new();
for (let sheetIndex = 0; sheetIndex < 8; sheetIndex++) {
  const rows = Array.from({length: 25000}, (_, row) =>
    Array.from({length: 20}, (_, column) => sheetIndex * 500000 + row * 20 + column)
  );
  XLSX.utils.book_append_sheet(workbook, XLSX.utils.aoa_to_sheet(rows), `Sheet${sheetIndex + 1}`);
}
const fixture = XLSX.write(workbook, {bookType: 'xlsx', type: 'buffer', compression: true});
console.log(`Fixture size: ${(fixture.byteLength / 1024 / 1024).toFixed(1)} MiB`);
let workerSource = await readFile(workerPath, 'utf8');
if (protocol === 'legacy') workerSource = workerSource.replaceAll('close();', 'setTimeout(close, 750);');
const vendorSource = await readFile(join(root, 'web/assets/file-vendor/xlsx.full.min.js'));
const server = http.createServer((request, response) => {
  if (request.url === '/fixture.xlsx') {
    response.writeHead(200, {'content-type': 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet', 'cache-control': 'no-store'});
    response.end(fixture);
  } else if (request.url === '/worker.js') {
    response.writeHead(200, {'content-type': 'text/javascript', 'cache-control': 'no-store'});
    response.end(workerSource);
  } else if (request.url === '/static/vendor/xlsx.full.min.js') {
    response.writeHead(200, {'content-type': 'text/javascript', 'cache-control': 'no-store'});
    response.end(vendorSource);
  } else {
    response.writeHead(200, {'content-type': 'text/html', 'cache-control': 'no-store'});
    response.end(`<!doctype html><meta charset="utf-8"><title>Spreadsheet preview benchmark</title><pre id="result">running</pre><script>
(async()=>{
 const legacy=${protocol === 'legacy'};
 const buffer=await fetch('/fixture.xlsx',{cache:'no-store'}).then(response=>response.arrayBuffer());
 const worker=new Worker('/worker.js');
 const started=performance.now();
 window.benchmarkResult=await new Promise((resolve,reject)=>{
  worker.onerror=()=>reject(Error('worker error'));
  worker.onmessage=event=>{
   const message=event.data||{};
   if(message.type==='error'||message.error)return reject(Error(message.error));
   const sheet=message.sheet||(message.sheets&&message.sheets[0]);
   if(!sheet)return;
   let hash=2166136261;
   for(const row of sheet.rows)for(const value of row){const text=String(value);for(let i=0;i<text.length;i++){hash^=text.charCodeAt(i);hash=Math.imul(hash,16777619)}}
   resolve({readyMs:performance.now()-started,rows:sheet.rows.length,totalRows:sheet.totalRows,totalColumns:sheet.totalColumns,truncated:sheet.truncated,firstRow:sheet.rows[0],lastRow:sheet.rows[sheet.rows.length-1],rowHash:(hash>>>0).toString(16)});
  };
  if(legacy)worker.postMessage(buffer,[buffer]);else worker.postMessage({buffer,format:'xlsx'},[buffer]);
 });
 document.getElementById('result').textContent=JSON.stringify(window.benchmarkResult);
 setTimeout(()=>worker.terminate(),750);
})().catch(error=>{window.benchmarkError=String(error);document.getElementById('result').textContent=window.benchmarkError});
</script>`);
  }
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
const url = `http://127.0.0.1:${server.address().port}/`;
const profile = await mkdtemp(join(tmpdir(), 'spreadsheet-preview-chrome-'));
const chromeProcess = spawn(chrome, [
  '--headless=new', '--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage',
  '--disable-background-networking', '--no-first-run', '--no-default-browser-check',
  '--remote-allow-origins=*', '--remote-debugging-port=0', `--user-data-dir=${profile}`, 'about:blank'
], {stdio: 'ignore'});

class DevTools {
  constructor(socket) {
    this.socket = socket;
    this.nextId = 0;
    this.pending = new Map();
    this.listeners = new Set();
    socket.addEventListener('message', event => {
      const message = JSON.parse(event.data);
      if (message.id && this.pending.has(message.id)) {
        const pending = this.pending.get(message.id);
        this.pending.delete(message.id);
        if (message.error) pending.reject(new Error(message.error.message));
        else pending.resolve(message.result || {});
      } else {
        for (const listener of this.listeners) listener(message);
      }
    });
  }
  send(method, params = {}, sessionId) {
    const id = ++this.nextId;
    return new Promise((resolve, reject) => {
      this.pending.set(id, {resolve, reject});
      this.socket.send(JSON.stringify({id, method, params, ...(sessionId ? {sessionId} : {})}));
    });
  }
  on(listener) { this.listeners.add(listener); return () => this.listeners.delete(listener); }
}

let devtools;
try {
  let port;
  const activePort = join(profile, 'DevToolsActivePort');
  for (let attempt = 0; attempt < 200; attempt++) {
    try { port = Number((await readFile(activePort, 'utf8')).split('\n')[0]); break; }
    catch { await new Promise(resolve => setTimeout(resolve, 50)); }
  }
  if (!port) throw new Error('Chrome did not publish a remote debugging port');
  const version = await fetch(`http://127.0.0.1:${port}/json/version`).then(response => response.json());
  const socket = new WebSocket(version.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => { socket.addEventListener('open', resolve, {once: true}); socket.addEventListener('error', reject, {once: true}); });
  devtools = new DevTools(socket);
  const results = [];

  for (let run = 0; run < runs; run++) {
    const {targetId} = await devtools.send('Target.createTarget', {url: 'about:blank'});
    const {sessionId: pageSession} = await devtools.send('Target.attachToTarget', {targetId, flatten: true});
    let workerSession;
    let peakBytes = 0;
    let sampling = false;
    const detach = devtools.on(message => {
      if (message.method !== 'Target.attachedToTarget' || message.params.targetInfo.type !== 'worker') return;
      workerSession = message.params.sessionId;
      devtools.send('Performance.enable', {}, workerSession).catch(() => {});
      devtools.send('Runtime.enable', {}, workerSession).catch(() => {});
    });
    await devtools.send('Target.setAutoAttach', {autoAttach: true, waitForDebuggerOnStart: false, flatten: true}, pageSession);
    await devtools.send('Page.enable', {}, pageSession);
    await devtools.send('Network.enable', {}, pageSession);
    await devtools.send('Network.setCacheDisabled', {cacheDisabled: true}, pageSession);
    await devtools.send('Runtime.enable', {}, pageSession);
    await devtools.send('Page.navigate', {url}, pageSession);
    let result;
    const deadline = Date.now() + 180000;
    while (Date.now() < deadline && !result) {
      const evaluation = await devtools.send('Runtime.evaluate', {expression: 'window.benchmarkResult||window.benchmarkError||null', returnByValue: true}, pageSession);
      result = evaluation.result && evaluation.result.value;
      if (!result) await new Promise(resolve => setTimeout(resolve, 50));
      if (workerSession && !sampling) {
        sampling = true;
        try {
          const metrics = await devtools.send('Performance.getMetrics', {}, workerSession);
          const used = metrics.metrics.find(metric => metric.name === 'JSHeapUsedSize');
          if (used) peakBytes = Math.max(peakBytes, used.value);
        } catch {
          try {
            const heap = await devtools.send('Runtime.getHeapUsage', {}, workerSession);
            peakBytes = Math.max(peakBytes, heap.usedSize || 0);
          } catch {}
        }
        sampling = false;
      }
    }
    await new Promise(resolve => setTimeout(resolve, 800));
    if (workerSession) {
      try {
        const metrics = await devtools.send('Performance.getMetrics', {}, workerSession);
        const used = metrics.metrics.find(metric => metric.name === 'JSHeapUsedSize');
        if (used) peakBytes = Math.max(peakBytes, used.value);
      } catch {}
    }
    detach();
    await devtools.send('Target.closeTarget', {targetId}).catch(() => {});
    if (!result || typeof result !== 'object') throw new Error(`Run ${run + 1} failed: ${String(result || 'benchmark timed out')}`);
    if (result.rows !== 5000 || result.totalRows !== 25000 || result.totalColumns !== 20 || result.truncated !== true || result.rowHash !== '188cadcb') {
      throw new Error(`Run ${run + 1} returned an incorrect preview: ${JSON.stringify(result)}`);
    }
    const record = {...result, peakWorkerHeapMiB: Number((peakBytes / 1024 / 1024).toFixed(1))};
    results.push(record);
    console.log(`Run ${run + 1}: ${JSON.stringify(record)}`);
  }

  const sorted = values => [...values].sort((left, right) => left - right);
  const median = values => sorted(values)[Math.floor(values.length / 2)];
  console.log(`Median worker ready: ${median(results.map(result => result.readyMs)).toFixed(1)} ms`);
  console.log(`Median peak worker JS heap: ${median(results.map(result => result.peakWorkerHeapMiB)).toFixed(1)} MiB`);
  console.log(`Preview signature: ${JSON.stringify({rows: results[0].rows,totalRows: results[0].totalRows,totalColumns: results[0].totalColumns,truncated: results[0].truncated,rowHash: results[0].rowHash})}`);
  if (results.some(result => result.peakWorkerHeapMiB <= 0)) throw new Error('Chrome did not report worker JavaScript heap usage');
} finally {
  devtools?.socket.close();
  chromeProcess.kill('SIGTERM');
  await new Promise(resolve => {
    if (chromeProcess.exitCode !== null) return resolve();
    chromeProcess.once('exit', resolve);
    setTimeout(resolve, 5000).unref();
  });
  await new Promise(resolve => server.close(resolve));
  await rm(profile, {recursive: true, force: true});
}
