// Development-only tests; Node is not used by the application or its plugins.
const {test} = require('node:test');
const assert = require('node:assert/strict');
const {readFileSync} = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = readFileSync(path.join(__dirname, '../templates/pages/plugin_pages.templ'), 'utf8').split('templ pluginHostBridgeScript()')[1].split('<script>')[1].split('</script>')[0];
const flush = () => new Promise(resolve => setImmediate(resolve));
function emitter() {
 const listeners = new Map();
 return {addEventListener(k,f){listeners.set(k,f)},removeEventListener(k,f){if(listeners.get(k)===f)listeners.delete(k)},emit(k,e){listeners.get(k)?.(e)}};
}
function setup() {
 const posts=[],requests=[],events=[],streams=[],timers=[];
 const frame={...emitter(),contentWindow:{postMessage(m){posts.push(m)}}};
 const approvals={innerHTML:''};
 const attrs={'data-plugin-handle':'handle','data-bridge-url':'/bridge','data-events-url':'/events?project_id=p','data-retire-url':'/retire?project_id=p','data-approvals-url':'/approvals','data-project-id':'p'};
 const root={setAttribute(k,v){attrs[k]=v},getAttribute(k){return attrs[k]},querySelector(k){return k==='#plugin-frame'?frame:k==='#plugin-approvals'?approvals:null}};
 const window={...emitter(),openVibelyLive:{execution(){const s={...emitter(),close(){s.closed=true}};streams.push(s);return s}}};
 const document={...emitter(),currentScript:{closest(){return root}}};
 const fetch=(url,opts)=>new Promise(resolve=>requests.push({url,opts,resolve}));
 function EventSource(){const s={...emitter(),close(){s.closed=true}};events.push(s);return s;}
 class DOMParser { parseFromString() { return {querySelector(){return {getAttribute(k){return k === "data-plugin-handle" ? "new-handle" : attrs[k]},querySelector(k){return k === "#plugin-frame" ? {getAttribute(){return "/new-frame"}} : null}}}}} }
 vm.runInNewContext(source,{DOMParser,window,document,fetch,EventSource,AbortController,console,setTimeout:(fn)=>{timers.push(fn);return timers.length-1},clearTimeout:(id)=>{timers[id]=null}});
 const send=(type,id='m')=>window.emit('message',{source:frame.contentWindow,data:{v:1,handle:'handle',id,type,payload:{}}});
 const respond=(req,result)=>req.resolve({ok:true,json:async()=>({ok:true,result}),text:async()=>result});
 return {posts,requests,events,streams,timers,frame,window,document,root,approvals,send,respond};
}

test('reconnect requests fresh page data; navigation closes streams and ignores late analysis',async()=>{
 const h=setup();h.send('events.subscribe');h.respond(h.requests.find(r=>r.url==='/bridge'),{});await flush();
 h.posts.length=0;
 h.events[0].onopen();assert.ok(h.posts.some(p=>p.type==='event'&&p.payload.type==='page.refresh'));
 h.send('analyze.request','late');const pending=h.requests.filter(r=>r.url==='/bridge').at(-1);
 h.document.emit('htmx:beforeSwap',{detail:{target:{id:'main-content'}}});
 assert.equal(h.events[0].closed,true);assert.equal(pending.opts.signal.aborted,true);
 const n=h.posts.length;h.respond(pending,{exec_id:'late'});await flush();
 assert.equal(h.streams.length,0);assert.equal(h.posts.length,n);
 assert.equal(h.requests.some(r=>r.url.startsWith('/retire')),true);
 assert.equal(h.requests.some(r=>r.url.includes('/stop')),false);
});

test('failed navigation keeps the current bridge alive', async()=>{
 const h=setup();
 h.document.emit('htmx:beforeSwap',{detail:{target:{id:'main-content'},shouldSwap:false}});
 assert.equal(h.events[0].closed,undefined);
 assert.equal(h.requests.some(r=>r.url.startsWith('/retire')),false);
 h.send('op.invoke');h.respond(h.requests.find(r=>r.url==='/bridge'),{value:1});await flush();
 assert.ok(h.posts.some(p=>p.type==='result'));
});

test('leaving detaches an active analysis stream without sending Stop',async()=>{
 const h=setup();h.send('analyze.request');h.respond(h.requests.find(r=>r.url==='/bridge'),{exec_id:'running'});await flush();
 assert.equal(h.streams.length,1);h.window.emit('pagehide');assert.equal(h.streams[0].closed,true);
 const n=h.posts.length;h.streams[0].emit('message',{data:'late token'});assert.equal(h.posts.length,n);
 assert.equal(h.requests.some(r=>r.url.includes('/stop')),false);
});

test('browser back-forward cache reloads a retired page capability',()=>{
 const h=setup();let reloads=0;h.window.location={reload(){reloads++}};
 h.window.emit('pagehide',{persisted:true});h.window.emit('pageshow',{persisted:true});
 assert.equal(reloads,1);
});

test('Sales ignores old report responses and publishes context before Analyze',async()=>{
 const outgoing=[];
 const parent={postMessage(m){outgoing.push(m)}};
 const window={...emitter(),parent};
 const element=()=>({...emitter(),value:'',style:{},textContent:'',appendChild(){}});
 const elements=new Map();
 const document={getElementById(id){if(!elements.has(id))elements.set(id,element());return elements.get(id)},createElement:element};
 vm.runInNewContext(readFileSync(path.join(__dirname,'../../internal/pageplugins/testdata/sales/app.js'),'utf8'),{window,document,location:{search:'?handle=h'},URLSearchParams,console});
 const reply=(msg,payload)=>window.emit('message',{source:parent,data:{v:1,handle:'h',type:'result',replyTo:msg.id,payload}});
 reply(outgoing.at(-1),{});await flush();reply(outgoing.at(-1),{});await flush();
 const first=outgoing.filter(m=>m.type==='op.invoke').at(-1);
 window.emit('message',{source:parent,data:{v:1,handle:'h',type:'event',payload:{type:'page.refresh'}}});
 const second=outgoing.filter(m=>m.type==='op.invoke').at(-1);
 assert.notEqual(first.id,second.id);
 reply(second,{revision:2,rows:[],target:10,generated_at:new Date().toISOString()});await flush();
 reply(first,{revision:1,rows:[],target:10,generated_at:new Date().toISOString()});await flush();
 assert.equal(document.getElementById('rev').textContent,'2');
 document.getElementById('analyze').emit('click');
 const context=outgoing.at(-1);assert.equal(context.type,'context.set');
 assert.equal(outgoing.some(m=>m.type==='analyze.request'),false);
 reply(context,{});await flush();assert.equal(outgoing.at(-1).type,'analyze.request');
});

 test('startup timeout reports failure; confirmed startup clears the timeout', async()=>{
  const h=setup();h.timers[0]();
  const request=h.requests.find(r=>r.url==='/bridge');
  assert.equal(JSON.parse(request.opts.body).type,'page.failed');
  const loaded=setup();loaded.send('page.loaded');
  loaded.respond(loaded.requests.find(r=>r.url==='/bridge'),{state:'loaded'});await flush();
  assert.equal(loaded.timers[0],null);
  const closed=setup();closed.window.emit('pagehide');assert.equal(closed.timers[0],null);
 });


test('page update refreshes only the frame and leaves chat mounted',async()=>{
 const h=setup(); let reloads=0;
 h.window.location={pathname:'/p/current',href:'/p/current',reload(){reloads++}};
 h.window.emit('plugin-page-updated',{detail:{task_id:'other'}});
 assert.equal(h.requests.length,0);
 h.window.emit('plugin-page-updated',{detail:{task_id:'current'}});
 h.respond(h.requests[0],'<html>updated</html>'); await flush();
 assert.equal(h.frame.src,'/new-frame');
 assert.equal(h.root.getAttribute('data-plugin-handle'),'new-handle');
 assert.equal(reloads,0);
 h.window.emit('pagehide',{});
 const count=h.requests.length;
 h.window.emit('plugin-page-updated',{detail:{task_id:'current'}});
 assert.equal(h.requests.length,count);
});
