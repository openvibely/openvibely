// Development-only browser test; the application does not require Node.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {mkdtemp, readFile, rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';

test('page overflow fits, resizes, navigates by keyboard and refreshes project links', async t => {
 const profile = await mkdtemp(join(tmpdir(), 'ov-page-overflow-'));
 const chrome = spawn(process.env.CHROME_BIN || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', ['--headless=new', '--no-sandbox', '--remote-debugging-port=0', '--user-data-dir='+profile, 'about:blank']);
 t.after(async () => { chrome.kill(); await new Promise(r => chrome.once('exit', r)); await rm(profile, {recursive:true,force:true}); });
 const endpoint = await new Promise((resolve,reject) => {
  let output=''; const timer=setTimeout(()=>reject(Error('Chrome startup timed out')),10000);
  chrome.once('error', e=>{clearTimeout(timer);reject(e);});
  chrome.stderr.on('data', d=>{output+=d;const m=output.match(/DevTools listening on (ws:\/\/[^\s]+)/);if(m){clearTimeout(timer);resolve(m[1]);}});
 });
 const origin = new URL(endpoint); const target = await (await fetch('http://'+origin.host+'/json/new?about:blank',{method:'PUT'})).json();
 const ws = new WebSocket(target.webSocketDebuggerUrl); await new Promise(r=>ws.onopen=r);
 let seq=0; const pending=new Map(); ws.onmessage=e=>{const m=JSON.parse(e.data);if(pending.has(m.id)){pending.get(m.id)(m.result);pending.delete(m.id);}};
 const send=(method,params={})=>new Promise(resolve=>{pending.set(++seq,resolve);ws.send(JSON.stringify({id:seq,method,params}));});
 const evaluate=async expression=>{const r=await send('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value;};
 const settle=()=>evaluate('new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(()=>requestAnimationFrame(r))))');
 await send('Emulation.setDeviceMetricsOverride',{width:1000,height:700,deviceScaleFactor:1,mobile:false});
 const source=await readFile(new URL('../assets/plugin-nav-overflow.js',import.meta.url),'utf8');
 await evaluate(`document.body.innerHTML = ${JSON.stringify(`<style>body{margin:0}aside{height:100vh;width:64px;display:flex;flex-direction:column}.sidebar-inner{flex:1;min-height:0;overflow:auto;padding:8px}ul{padding:0;margin:0;list-style:none}li{height:40px}.sidebar-aside.sidebar-collapsed .menu li:not(.sidebar-hide-collapsed):not(.sidebar-divider){display:flex!important}#sidebar #plugin-pages-nav [hidden]{display:none!important}footer{height:50px;flex-shrink:0}#builtin{height:300px}#plugin-pages-overflow-menu{background:white}</style><aside id="sidebar" class="sidebar-aside sidebar-collapsed"><div class="sidebar-inner"><div id="builtin">Built-ins</div><ul id="plugin-pages-nav" class="menu">${Array.from({length:18},(_,i)=>`<li data-plugin-nav-entry="${i}"><a href="/p/${i}?project_id=alpha" hx-get="/p/${i}?project_id=alpha" hx-target="#main-content"><span class="sidebar-text">Page ${i} · Alpha</span></a></li>`).join('')}<li data-plugin-nav-overflow hidden><button aria-expanded="false">⋯</button></li></ul></div><footer>Settings</footer></aside>`)};`);
 await evaluate(source); await settle();
 const count=()=>evaluate("document.querySelectorAll('[data-plugin-nav-entry][hidden]').length");
 const initial=await count(); assert.ok(initial>0 && initial<18);
 assert.equal(await evaluate("document.querySelector('[data-plugin-nav-overflow]').getBoundingClientRect().bottom <= document.querySelector('footer').getBoundingClientRect().top"),true);
 await evaluate("document.querySelector('[data-plugin-nav-overflow] button').click()"); await settle();
 assert.equal(await evaluate("document.querySelectorAll('#plugin-pages-overflow-menu a').length"),initial);
 await evaluate("document.dispatchEvent(new CustomEvent('htmx:afterSwap',{detail:{target:document.querySelector('footer')}}))");await settle();
 assert.equal(await evaluate("document.querySelector('[data-plugin-nav-overflow] button').getAttribute('aria-expanded')"),'true');
 assert.match(await evaluate('document.activeElement.textContent'),/Alpha/);
 assert.match(await evaluate("document.activeElement.getAttribute('hx-get')"),/project_id=alpha/);
 await send('Input.dispatchKeyEvent',{type:'keyDown',key:'End'});
 assert.match(await evaluate('document.activeElement.textContent'),/Page 17/);
 await send('Input.dispatchKeyEvent',{type:'keyDown',key:'Escape'});
 assert.equal(await evaluate("document.activeElement.closest('[data-plugin-nav-overflow]') !== null"),true);
 await send('Emulation.setDeviceMetricsOverride',{width:1000,height:1000,deviceScaleFactor:1,mobile:false});await settle();assert.ok(await count()<initial);
 await evaluate("document.querySelectorAll('[data-plugin-nav-entry]').forEach((e,i)=>{if(i>1)e.remove();else {e.querySelector('a').textContent='Beta '+i;e.querySelector('a').setAttribute('hx-get','/p/'+i+'?project_id=beta');}})");await settle();
 assert.equal(await count(),0);assert.equal(await evaluate("document.querySelector('[data-plugin-nav-overflow]').hidden"),true);
 ws.close();
});
