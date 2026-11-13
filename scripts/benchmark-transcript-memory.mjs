#!/usr/bin/env node
// Node 22+; no npm dependencies. Uses an isolated Chrome profile per trial.
// Usage: node scripts/benchmark-transcript-memory.mjs BASELINE_JS OUTPUT_JSON [PAIRS=10]
// CHROME_PATH overrides the Chrome executable. This measures the virtual-list
// fixture, not the whole application or peak allocation during a scroll frame.
import {readFile, writeFile, mkdtemp, rm} from 'node:fs/promises';
import {createServer} from 'node:http';
import {spawn, execFileSync} from 'node:child_process';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {createHash} from 'node:crypto';
import {once} from 'node:events';

const [baselinePath, outputPath, pairsArg = '10'] = process.argv.slice(2);
const pairs = Number(pairsArg);
if (!baselinePath || !outputPath || !Number.isInteger(pairs) || pairs < 1) {
    throw Error('Usage: node scripts/benchmark-transcript-memory.mjs BASELINE_JS OUTPUT_JSON [PAIRS=10]');
}
const chromePath = process.env.CHROME_PATH || (process.platform === 'darwin'
    ? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' : 'chromium');
const sources = {
    baseline: await readFile(baselinePath, 'utf8'),
    current: await readFile(new URL('../web/assets/chat-virtual-list.js', import.meta.url), 'utf8'),
};
const fixture = version => `<!doctype html><style>
body{margin:0}#messages{height:900px;overflow:auto;width:800px}
[data-chat-lazy-row]{box-sizing:border-box}
</style><div id="messages" data-chat-transcript><div id="owner"></div></div>
<script src="/${version}.js"></script><script>
window.paint=()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
window.setup=async count=>{
 const owner=document.getElementById('owner');
 await installChatVirtualRows(owner,{ungrouped:true,models:Array.from({length:count},(_,i)=>({
  kind:'text',height:100,width:800,segments:[{content:'Row '+i}]
 }))},(node,model,index)=>{node.style.height='100px';node.textContent='Row '+index});
 await paint();await paint();
};
window.scrollFixture=async count=>{
 const messages=document.getElementById('messages'),owner=document.getElementById('owner');
 const max=count*100-900;
 const sequence=[0,300,600,900,600,900,1800,max*.25,max*.25+300,max*.5,max*.5+300,max*.5-300,max*.75,max,max-300,max-600,max-300,max*.75,max*.5,max*.25,1800,900,1200,600,0];
 for(const top of sequence){
  messages.scrollTop=top;await paint();
  if(Math.abs(messages.scrollTop-top)>1)throw Error('Scroll offset changed at '+top);
  for(let i=Math.floor(messages.scrollTop/100);i<Math.min(count,Math.ceil((messages.scrollTop+900)/100));i++){
   const el=owner.children[i];
   if(!el||el.hasAttribute('data-virtual-chat-row')||el.textContent!=='Row '+i)throw Error('Missing visible row '+i);
  }
 }
 await paint();
 if(owner.querySelectorAll('[data-chat-lazy-row]:not([data-virtual-chat-row])').length>=40)throw Error('Mounted row bound exceeded');
};
window.removeFixture=async()=>{
 document.getElementById('messages').remove();
 refreshVirtualChatRows(document.body);await paint();await paint();
};
window.fixtureReady=true;
</script>`;
const server = createServer((req, res) => {
    const url = new URL(req.url, 'http://localhost');
    const version = url.searchParams.get('version');
    if (url.pathname === '/fixture' && Object.hasOwn(sources, version)) {
        res.setHeader('Content-Type', 'text/html');res.end(fixture(version));
    } else if (url.pathname === '/baseline.js' || url.pathname === '/current.js') {
        res.setHeader('Content-Type', 'text/javascript');res.end(sources[url.pathname.slice(1, -3)]);
    } else {res.statusCode = 404;res.end();}
});
server.listen(0, '127.0.0.1');
await once(server, 'listening');
const origin = `http://127.0.0.1:${server.address().port}`;
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));

class CDP {
    constructor(socket) {
        this.socket = socket;this.next = 0;this.pending = new Map();
        socket.addEventListener('message', event => {
            const message = JSON.parse(event.data);
            if (!message.id) return;
            const pending = this.pending.get(message.id);
            if (!pending) return;
            this.pending.delete(message.id);clearTimeout(pending.timer);
            if (message.error) pending.reject(Error(JSON.stringify(message.error)));
            else pending.resolve(message.result);
        });
    }
    send(method, params = {}, sessionId) {
        return new Promise((resolve, reject) => {
            const id = ++this.next;
            const timer = setTimeout(() => {this.pending.delete(id);reject(Error('CDP timeout: '+method));}, 30000);
            this.pending.set(id, {resolve, reject, timer});
            this.socket.send(JSON.stringify({id, method, params, ...(sessionId ? {sessionId} : {})}));
        });
    }
    close() {
        for (const pending of this.pending.values()) {clearTimeout(pending.timer);pending.reject(Error('CDP closed'));}
        this.pending.clear();this.socket.close();
    }
}

async function trial(version, count) {
    const profile = await mkdtemp(join(tmpdir(), 'ov-transcript-memory-'));
    let chrome, client;
    try {
        chrome = spawn(chromePath, [
            '--headless=new', '--no-sandbox', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
            '--disable-background-networking', '--disable-component-update', '--disable-background-timer-throttling',
            '--disable-renderer-backgrounding', '--disable-extensions', '--renderer-process-limit=1',
            '--disable-features=SpareRendererForSitePerProcess', '--window-size=1100,1100', '--remote-debugging-port=0',
            '--use-mock-keychain', `--user-data-dir=${profile}`, 'about:blank',
        ], {stdio:['ignore', 'ignore', 'pipe']});
        const endpoint = await new Promise((resolve, reject) => {
            let stderr = '';
            const timer = setTimeout(() => reject(Error('Chrome startup timeout: '+stderr.slice(-2000))), 15000);
            const onData = chunk => {
                stderr += chunk.toString();
                const match = stderr.match(/DevTools listening on (ws:\/\/\S+)/);
                if (match) {clearTimeout(timer);chrome.stderr.off('data', onData);resolve(match[1]);}
            };
            chrome.stderr.on('data', onData);
            chrome.once('error', error => {clearTimeout(timer);reject(error);});
            chrome.once('exit', code => {clearTimeout(timer);reject(Error('Chrome exited: '+code));});
        });
        chrome.stderr.resume();
        const socket = new WebSocket(endpoint);
        await new Promise((resolve, reject) => {socket.addEventListener('open', resolve, {once:true});socket.addEventListener('error', reject, {once:true});});
        client = new CDP(socket);
        const browser = await client.send('Browser.getVersion');
        const {targetInfos} = await client.send('Target.getTargets');
        const target = targetInfos.find(target => target.type === 'page');
        if (!target) throw Error('Missing Chrome page');
        const {sessionId} = await client.send('Target.attachToTarget', {targetId:target.targetId, flatten:true});
        const send = (method, params) => client.send(method, params, sessionId);
        const evaluate = async expression => {
            const result = await send('Runtime.evaluate', {expression, awaitPromise:true, returnByValue:true});
            if (result.exceptionDetails) throw Error(JSON.stringify(result.exceptionDetails));
            return result.result.value;
        };
        await send('Page.navigate', {url:`${origin}/fixture?version=${version}`});
        let ready = false;
        for (let attempt=0; attempt<100; attempt++) {
            if (await evaluate('window.fixtureReady === true')) {ready = true;break;}
            await delay(50);
        }
        if (!ready) throw Error('Fixture failed to load');
        const snapshot = async () => {
            await evaluate('paint()');
            await send('HeapProfiler.collectGarbage');
            await send('HeapProfiler.collectGarbage');
            const heap = await send('Runtime.getHeapUsage');
            const dom = await send('Memory.getDOMCounters');
            const {processInfo} = await client.send('SystemInfo.getProcessInfo');
            const renderers = processInfo.filter(process => process.type === 'renderer');
            if (!renderers.length) throw Error('No renderer processes');
            // ps reports resident KiB on macOS/Linux. Sum only renderers belonging
            // to this fresh benchmark browser, not other installed/running apps.
            const rss = execFileSync('ps', ['-o', 'rss=', '-p', renderers.map(process => process.id).join(',')], {encoding:'utf8'});
            const rssKiB = rss.trim().split(/\s+/).map(Number);
            if (rssKiB.length !== renderers.length || rssKiB.some(value => !Number.isFinite(value))) throw Error('Invalid renderer RSS');
            return {...heap, ...dom, rendererRSSBytes:rssKiB.reduce((sum, value)=>sum+value,0)*1024,
                rendererProcesses:renderers.length,
                mounted:await evaluate("document.querySelectorAll('[data-chat-lazy-row]:not([data-virtual-chat-row])').length")};
        };
        const empty = await snapshot();
        await evaluate(`setup(${count})`);
        const initial = await snapshot();
        await evaluate(`scrollFixture(${count})`);
        const scrolled = await snapshot();
        await evaluate('removeFixture()');
        const removed = await snapshot();
        return {version, count, browser:browser.product, empty, initial, scrolled, removed};
    } finally {
        client?.close();
        if (chrome && chrome.exitCode === null) {
            const stopped = once(chrome, 'exit');chrome.kill('SIGTERM');
            const timer = setTimeout(()=>chrome.kill('SIGKILL'), 5000);
            await stopped;clearTimeout(timer);
        }
        await rm(profile, {recursive:true, force:true});
    }
}
const median = values => {
    const sorted = [...values].sort((a,b)=>a-b), middle = Math.floor(sorted.length/2);
    return sorted.length%2 ? sorted[middle] : (sorted[middle-1]+sorted[middle])/2;
};
const metrics = ['usedSize', 'embedderHeapUsedSize', 'backingStorageSize', 'rendererRSSBytes', 'nodes', 'jsEventListeners', 'rendererProcesses', 'mounted'];
const results = {pairs, platform:process.platform, arch:process.arch,
    hashes:Object.fromEntries(Object.entries(sources).map(([name, source])=>[name, createHash('sha256').update(source).digest('hex')])),
    methodology:'Fresh Chrome process/profile per trial; alternating paired versions; two forced GCs per snapshot. Bytes unless a counter. usedSize is V8 heap; rendererRSSBytes is summed renderer RSS, not total app memory.',
    trials:[], summary:[]};
try {
    for (const count of [200, 1000, 5000]) {
        for (let pair=0; pair<pairs; pair++) {
            for (const version of (pair%2 ? ['current', 'baseline'] : ['baseline', 'current'])) {
                results.trials.push({pair, ...await trial(version, count)});
                await writeFile(outputPath, JSON.stringify(results, null, 2)+'\n');
            }
            console.log(`${count} rows: pair ${pair+1}/${pairs} complete`);
        }
        for (const stage of ['initial', 'scrolled', 'removed']) {
            const summary = {count, stage, baseline:{}, current:{}, pairedDelta:{}};
            for (const metric of metrics) {
                if (!results.trials.every(run=>typeof run[stage][metric] === 'number')) continue;
                for (const version of ['baseline', 'current']) {
                    const runs = results.trials.filter(run=>run.count===count && run.version===version);
                    summary[version][metric] = median(runs.map(run=>run[stage][metric]));
                    if (metric.endsWith('Size') || metric.endsWith('Bytes')) {
                        summary[version][metric+'AboveEmpty'] = median(runs.map(run=>run[stage][metric]-run.empty[metric]));
                    }
                }
                const deltas = Array.from({length:pairs}, (_,pair)=>{
                    const before = results.trials.find(run=>run.count===count && run.version==='baseline' && run.pair===pair);
                    const after = results.trials.find(run=>run.count===count && run.version==='current' && run.pair===pair);
                    return after[stage][metric]-before[stage][metric];
                });
                summary.pairedDelta[metric] = median(deltas);
            }
            results.summary.push(summary);
        }
        await writeFile(outputPath, JSON.stringify(results, null, 2)+'\n');
    }
} finally {server.close();}
console.log('Results: '+outputPath);
