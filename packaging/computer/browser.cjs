// Runs inside the computer. Browser credentials never appear in tool results.
const {chromium}=require('/opt/browser/node_modules/playwright');
const {createHash}=require('node:crypto');
async function snapshot(page) {
 const view=await page.evaluate(()=>{
function computerUUID() {
 const cryptoAPI = globalThis.crypto;
 if (typeof cryptoAPI.randomUUID === 'function') return cryptoAPI.randomUUID();
 const bytes = cryptoAPI.getRandomValues(new Uint8Array(16));
 bytes[6] = (bytes[6] & 15) | 64;
 bytes[8] = (bytes[8] & 63) | 128;
 const hex = Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('');
 return hex.slice(0, 8) + '-' + hex.slice(8, 12) + '-' + hex.slice(12, 16) + '-' + hex.slice(16, 20) + '-' + hex.slice(20);
}
  const state=window.__ovBrowserRefs ||= {nodes:new Map(),ids:new WeakMap(),document:computerUUID()};
  const secret=n=>/password|one[- ]?time|verification[ -]?code|security[ -]?code|passcode|\bpin\b|\btotp\b|\botp\b|\b2fa\b/i.test([n.type,n.autocomplete,n.name,n.id,n.getAttribute('aria-label'),n.getAttribute('placeholder'),...[...(n.labels||[])].map(l=>l.innerText)].join(' '));
  const active=new Map();
  const controls=[...document.querySelectorAll('a,button,input,select,textarea,[role="button"],[contenteditable="true"]')].filter(n=>n.getClientRects().length).slice(0,100).map(n=>{
   let ref=state.ids.get(n);if(!ref){ref=computerUUID();state.ids.set(n,ref);}active.set(ref,n);
   return {ref,tag:n.tagName.toLowerCase(),text:(n.innerText||n.getAttribute('aria-label')||'').slice(0,200),id:n.id,name:n.getAttribute('name'),type:n.getAttribute('type'),href:n.getAttribute('href'),disabled:!!n.disabled,sensitive:secret(n)};
  });
  state.nodes=active;
  return {document:state.document,url:location.href,title:document.title,text:(document.body?.innerText||'').slice(0,20000),controls,focused_sensitive:!!document.activeElement&&secret(document.activeElement)};
 });
 return {...view,snapshot:createHash('sha256').update(JSON.stringify(view)).digest('hex')};
}
async function run(args) {
 const browser=await chromium.connectOverCDP('http://127.0.0.1:'+process.env.OPENVIBELY_CDP_PORT);
 try {
  const context=browser.contexts()[0];
  if(args.action==='clear_cookies'){for(const c of args.cookies)await context.clearCookies({domain:c.domain,name:c.name,path:c.path});return {cleared:true};}
  if(args.action==='import_cookies'){await context.addCookies(args.cookies);return {imported:args.cookies.length};}
  const page=context.pages().find(p=>!p.url().startsWith('devtools:'))||await context.newPage();
  page.setDefaultTimeout(10000);
  let target;
  if(['click','type','select','press_key'].includes(args.action)) {
   const current=await snapshot(page);
   if(!args.snapshot||args.snapshot!==current.snapshot)throw Error('Page changed. Read the page again before acting.');
   if(args.action==='press_key') {
    if(!/^(Enter|Tab|Escape|Backspace|Delete|ArrowUp|ArrowDown|ArrowLeft|ArrowRight|Home|End|PageUp|PageDown|Space)$/.test(args.key)||current.focused_sensitive)throw Error('Use a private login handoff for passwords or verification codes.');
   } else {
    const control=current.controls.find(c=>c.ref===args.ref);
    if(!control)throw Error('Read the page and use a current control reference.');
    if(['type','select'].includes(args.action)&&control.sensitive)throw Error('Use a private login handoff for passwords or verification codes.');
    const handle=await page.evaluateHandle(ref=>window.__ovBrowserRefs.nodes.get(ref),args.ref);
    target=handle.asElement();if(!target)throw Error('Control changed. Read the page again.');
   }
  }
  switch(args.action){
   case 'verify_access':
   case 'navigate': {const url=new URL(args.url);if(!['http:','https:'].includes(url.protocol)||url.username||url.password)throw Error('HTTP URL required');const response=await page.goto(url.href,{waitUntil:'domcontentloaded',timeout:20000});if(args.action==='verify_access'&&(!response||!response.ok()))throw Error('Page inaccessible');break;}
   case 'read':break;
   case 'back':await page.goBack({waitUntil:'domcontentloaded'});break;
   case 'forward':await page.goForward({waitUntil:'domcontentloaded'});break;
   case 'click':await target.click();break;
   case 'type':await target.fill(args.text);break;
   case 'select':await target.selectOption(args.value);break;
   case 'press_key':await page.keyboard.press(args.key);break;
   case 'scroll':await page.mouse.wheel(0,Math.max(-2000,Math.min(2000,Number(args.amount)||600)));break;
   default:throw Error('Unsupported browser action');
  }
  const result=await snapshot(page);
  if(args.action==='verify_access'){
   if(new URL(page.url()).origin!==new URL(args.url).origin||result.controls.some(c=>c.sensitive))throw Error('Access not verified');
   if(typeof args.expected_text==='string'&&args.expected_text.trim()&&result.text.includes(args.expected_text))return {verified:true};
   return {verified:false,url:page.url(),screenshot:(await page.screenshot({type:'png'})).toString('base64')};
  }
  return result;
 } finally {await browser.close();}
}
module.exports={run};
if(require.main===module)(async()=>{let raw='';for await(const chunk of process.stdin)raw+=chunk;process.stdout.write(JSON.stringify(await run(JSON.parse(raw))));})().catch(()=>{process.stderr.write('Browser operation failed. Read the page again; use private login for credentials.');process.exitCode=1;});
