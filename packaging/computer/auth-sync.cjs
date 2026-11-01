// Project-only sign-in sharing. Values never leave this container or enter chat.
// Merge changes, including deletions, instead of replacing live profiles.
const {chromium}=require('/opt/browser/node_modules/playwright');
const fs=require('fs');
const file=process.env.HOME+'/.shared-browser-auth.json';
let saved={cookies:{},origins:{}};try{saved=JSON.parse(fs.readFileSync(file,'utf8'))}catch{}
let persisted=JSON.stringify(saved);
const peers=new Map();
const key=c=>JSON.stringify([c.domain,c.path,c.name,c.partitionKey||'']);
const eq=(a,b)=>JSON.stringify(a)===JSON.stringify(b);
function merge(now,before,shared){for(const k of new Set([...Object.keys(now),...Object.keys(before||{})])){if(before? !eq(now[k],before[k]):!(k in shared)) shared[k]=now[k]??null;}}
async function capture(context){
 const origins={},frames=new Map();
 for(const page of context.pages())for(const frame of page.frames()){try{const result=await frame.evaluate(()=>({origin:location.origin,values:Object.fromEntries(Object.entries(localStorage))}));if(/^https?:/.test(result.origin)){origins[result.origin]=result.values;frames.set(frame,result)}}catch{}}
 return {cookies:Object.fromEntries((await context.cookies()).map(c=>[key(c),c])),origins,frames};
}
async function sync(name,port){
 let p=peers.get(name);if(!p||p.port!==port||!p.browser.isConnected()){const browser=await chromium.connectOverCDP('http://127.0.0.1:'+port,{timeout:1500});p={browser,port,scripts:new Map()};peers.set(name,p)}
 const context=p.browser.contexts()[0];const state=await capture(context);
 const cookies=state.cookies;let changed=false;
 merge(cookies,p.cookies,saved.cookies);
 const origins=state.origins;
 for(const origin of Object.keys(origins)){saved.origins[origin]??={};merge(origins[origin]||{},p.origins?.[origin],saved.origins[origin])}
 // Cookie deletion is scoped to the exact cookie; never clear another login.
 for(const [k,cookie] of Object.entries(saved.cookies)){if(cookie===null&&cookies[k]){const c=cookies[k];await context.clearCookies({name:c.name,domain:c.domain,path:c.path});delete cookies[k];changed=true}}
 const incoming=Object.entries(saved.cookies).filter(([k,c])=>c&&!eq(c,cookies[k])).map(([,c])=>c);
 if(incoming.length){await context.addCookies(incoming);changed=true}
 // Keep storage for open origins in sync; install one replaceable preload per
 // page so subsequent navigation receives shared storage before app scripts.
 for(const page of context.pages()){
  let entry=p.scripts.get(page);if(!entry){entry={cdp:await context.newCDPSession(page),data:''};p.scripts.set(page,entry)}
  const data=JSON.stringify(saved.origins);
  const apply=stores=>{const values=stores[location.origin];if(!values)return;for(const [k,v] of Object.entries(values)){if(v===null)localStorage.removeItem(k);else localStorage.setItem(k,v)}};
  if(entry.data!==data){if(entry.id)await entry.cdp.send('Page.removeScriptToEvaluateOnNewDocument',{identifier:entry.id});const added=await entry.cdp.send('Page.addScriptToEvaluateOnNewDocument',{source:'('+apply.toString()+')('+data+');'});entry.id=added.identifier;entry.data=data}
  for(const frame of page.frames()){
   const observed=state.frames.get(frame),values=observed&&saved.origins[observed.origin];
   if(!values)continue;
   const differs=Object.entries(values).some(([k,v])=>v===null?k in observed.values:observed.values[k]!==v);
   if(differs){await frame.evaluate(apply,saved.origins).catch(()=>{});changed=true}
  }
 }
 for(const [page] of p.scripts)if(page.isClosed())p.scripts.delete(page);
 const after=changed?await capture(context):state;p.cookies=after.cookies;p.origins={...p.origins,...after.origins};p.initialized=true;
}
// A page paused by authentication or an unresponsive renderer must not hold
// the project-wide synchronization queue forever. Closing a CDP connection
// disconnects this helper; it does not close the user's remote browser.
async function syncBounded(name,port){
 let timer;
 try{await Promise.race([sync(name,port),new Promise((_,reject)=>{timer=setTimeout(()=>{
  const peer=peers.get(name);if(peer){peers.delete(name);peer.browser.close().catch(()=>{})}
  reject(Error('Browser synchronization timed out'));
 },4000)})])}finally{clearTimeout(timer)}
}
async function syncAll(){
 const res=await fetch('http://127.0.0.1:8090/desktop-sessions',{signal:AbortSignal.timeout(3000)});
 if(!res.ok)throw Error('Desktop list unavailable');
 const sessions=await res.json();
 await Promise.allSettled(Object.entries(sessions).filter(([name])=>!name.startsWith("task-")).map(([name,port])=>syncBounded(name,port)));
 for(const name of peers.keys())if(!(name in sessions)){const peer=peers.get(name);peers.delete(name);peer.browser.close().catch(()=>{})}
 const data=JSON.stringify(saved);if(data!==persisted){fs.writeFileSync(file+'.tmp',data,{mode:0o600});fs.renameSync(file+'.tmp',file);persisted=data}
}
let pending=Promise.resolve();function synchronize(){const next=pending.then(syncAll);pending=next.catch(()=>{});return next}
async function cycle(){try{await synchronize()}catch{}setTimeout(cycle,1500)}
require("http").createServer(async(req,res)=>{const requested=new URL(req.url,"http://localhost");if(requested.pathname!=="/sync"||req.headers.origin){res.writeHead(404);res.end();return}try{await synchronize();if(requested.searchParams.has("desktop")&&!peers.get(requested.searchParams.get("desktop"))?.initialized)throw Error("Starting");res.writeHead(204);res.end()}catch{res.writeHead(503);res.end("Sign-in synchronization is starting")}}).listen(8092,"127.0.0.1");cycle();
