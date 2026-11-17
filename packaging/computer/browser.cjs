// Runs inside the computer. Browser credentials never appear in tool results.
const {chromium}=require('/opt/browser/node_modules/playwright');
const {createHash}=require('node:crypto');
const fs=require('node:fs/promises');
const path=require('node:path');
const tabStatePath=()=>path.join(process.env.HOME,'.ov-browser-selected-tab');
async function tabId(page) {
 const session=await page.context().newCDPSession(page);
 try{return (await session.send('Target.getTargetInfo')).targetInfo.targetId;}finally{await session.detach();}
}
async function tabs(context) {
 return Promise.all(context.pages().filter(p=>!p.url().startsWith('devtools:')).map(async page=>({page,id:await tabId(page)})));
}
function buildLocator(root, spec, depth=0) {
 if(!spec||typeof spec!=='object'||depth>8)throw Error('Invalid locator');
 const keys=['selector','role','text','label','placeholder','test_id'].filter(k=>typeof spec[k]==='string');
 if(keys.length!==1)throw Error('Locator requires exactly one selector, role, text, label, placeholder or test_id');
 const exact=spec.exact===true;
 let result;
 switch(keys[0]){
  case 'selector':result=root.locator(spec.selector);break;
  case 'role':result=root.getByRole(spec.role,{name:spec.name,exact});break;
  case 'text':result=root.getByText(spec.text,{exact});break;
  case 'label':result=root.getByLabel(spec.label,{exact});break;
  case 'placeholder':result=root.getByPlaceholder(spec.placeholder,{exact});break;
  case 'test_id':result=root.getByTestId(spec.test_id);break;
 }
 if(spec.has_text!==undefined)result=result.filter({hasText:spec.has_text});
 if(spec.has_not_text!==undefined)result=result.filter({hasNotText:spec.has_not_text});
 if(spec.visible!==undefined)result=result.filter({visible:spec.visible});
 if(spec.has)result=result.filter({has:buildLocator(root,spec.has,depth+1)});
 if(spec.has_not)result=result.filter({hasNot:buildLocator(root,spec.has_not,depth+1)});
 if(spec.and)result=result.and(buildLocator(root,spec.and,depth+1));
 if(spec.or)result=result.or(buildLocator(root,spec.or,depth+1));
 if(spec.child)result=buildLocator(result,spec.child,depth+1);
 if(spec.index!==undefined){if(!Number.isInteger(spec.index)||spec.index<0)throw Error('Locator index must be nonnegative');result=result.nth(spec.index);}
 return result;
}
function locatorRoot(page,frames=[]) {
 if(!Array.isArray(frames)||frames.length>8||frames.some(f=>typeof f!=='string'||!f))throw Error('Invalid frame selectors');
 return frames.reduce((root,selector)=>root.frameLocator(selector),page);
}
async function assertPublicTarget(target) {
 const sensitive=await target.evaluate(n=>/password|one[- ]?time|verification[ -]?code|security[ -]?code|passcode|\bpin\b|\btotp\b|\botp\b|\b2fa\b/i.test([n.type,n.autocomplete,n.name,n.id,n.getAttribute('aria-label'),n.getAttribute('placeholder'),...[...(n.labels||[])].map(l=>l.innerText)].join(' ')));
 if(sensitive)throw Error('Use a private login handoff for passwords or verification codes.');
}
function pageRange(args){
 const offset=args.offset??0,limit=args.limit??100;
 if(!Number.isInteger(offset)||offset<0||!Number.isInteger(limit)||limit<1||limit>500)throw Error('Use offset >= 0 and limit between 1 and 500');
 return {offset,limit};
}
async function findElements(page,args){
 const locator=buildLocator(locatorRoot(page,args.frames),args.locator);
 const {offset,limit}=pageRange(args),count=await locator.count(),elements=[];
 for(let index=offset;index<Math.min(count,offset+limit);index++){
  const item=locator.nth(index);
  elements.push(await item.evaluate((n,index)=>({index,tag:n.tagName.toLowerCase(),text:(n.innerText||n.textContent||'').slice(0,1000),role:n.getAttribute('role'),name:n.getAttribute('aria-label'),visible:!!n.getClientRects().length,disabled:!!n.disabled}),index));
 }
 return {count,offset,elements,next_offset:offset+elements.length<count?offset+elements.length:null};
}
async function snapshot(page,range={offset:0,limit:100},textOffset=0) {
 const view=await page.evaluate(({range,textOffset})=>{
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
  const controls=[...document.querySelectorAll('a,button,input,select,textarea,[role="button"],[contenteditable="true"]')].filter(n=>n.getClientRects().length);
  const total=controls.length;
  const items=controls.slice(range.offset,range.offset+range.limit).map(n=>{
   let ref=state.ids.get(n);if(!ref){ref=computerUUID();state.ids.set(n,ref);}active.set(ref,n);
   return {ref,tag:n.tagName.toLowerCase(),text:(n.innerText||n.getAttribute('aria-label')||'').slice(0,200),id:n.id,name:n.getAttribute('name'),type:n.getAttribute('type'),href:n.getAttribute('href'),disabled:!!n.disabled,sensitive:secret(n)};
  });
  state.nodes=active;
  const text=document.body?.innerText||'';
  return {document:state.document,url:location.href,title:document.title,text:text.slice(textOffset,textOffset+20000),text_offset:textOffset,next_text_offset:textOffset+20000<text.length?textOffset+20000:null,controls:items,total_controls:total,offset:range.offset,next_offset:range.offset+items.length<total?range.offset+items.length:null,focused_sensitive:!!document.activeElement&&secret(document.activeElement)};
 },{range,textOffset});
 return {...view,snapshot:createHash('sha256').update(JSON.stringify(view)).digest('hex')};
}
async function run(args) {
 const browser=await chromium.connectOverCDP('http://127.0.0.1:'+process.env.OPENVIBELY_CDP_PORT);
 try {
  const context=browser.contexts()[0];
  if(args.action==='clear_cookies'){for(const c of args.cookies)await context.clearCookies({domain:c.domain,name:c.name,path:c.path});return {cleared:true};}
  if(args.action==='import_cookies'){await context.addCookies(args.cookies);return {imported:args.cookies.length};}
  let available=await tabs(context);
  if(args.action==='tabs')return {tabs:await Promise.all(available.map(async t=>({tab_id:t.id,url:t.page.url(),title:await t.page.title()})))};
  if(args.action==='open_tab'){const page=await context.newPage();const id=await tabId(page);await fs.writeFile(tabStatePath(),id,{mode:0o600});return {...await snapshot(page),tab_id:id};}
  let selected;try{selected=await fs.readFile(tabStatePath(),'utf8');}catch(e){if(e.code!=='ENOENT')throw e;}
  let entry=args.tab_id?available.find(t=>t.id===args.tab_id):available.find(t=>t.id===selected)||available[0];
  if(args.tab_id&&!entry)throw Error('Tab not found. List tabs again.');
  const page=entry?.page||await context.newPage();
  const id=entry?.id||await tabId(page);
  if(args.action==='close_tab'){if(!args.tab_id)throw Error('close_tab requires tab_id');await page.close();return {closed:true,tab_id:id};}
  if(args.action==='select_tab'){await fs.writeFile(tabStatePath(),id,{mode:0o600});await page.bringToFront();}

  const timeout=args.timeout_ms??10000;
  if(!Number.isInteger(timeout)||timeout<1||timeout>20000)throw Error('timeout_ms must be between 1 and 20000');
  page.setDefaultTimeout(timeout);page.setDefaultNavigationTimeout(timeout);
  if(args.action==='screenshot'){
   if(args.full_page&&args.clip)throw Error('Choose full_page or clip');
   if(args.clip&&(!['x','y','width','height'].every(k=>Number.isFinite(args.clip[k]))||args.clip.x<0||args.clip.y<0||args.clip.width<=0||args.clip.height<=0))throw Error('Invalid screenshot clip');
   const size=args.clip||await page.evaluate(full=>({width:innerWidth,height:full?Math.max(document.documentElement.scrollHeight,innerHeight):innerHeight}),!!args.full_page);
   if(size.width*size.height>32000000)throw Error('Screenshot is too large; use a smaller clip');
   const image=await page.screenshot({type:'jpeg',quality:80,fullPage:!!args.full_page,clip:args.clip,scale:'css',timeout});
   if(image.length>8000000)throw Error('Screenshot is too large; use a smaller clip');
   return {content:[{type:'image',mimeType:'image/jpeg',data:image.toString('base64')}]};
  }
  if(args.action==='wait_element'){
   const locator=buildLocator(locatorRoot(page,args.frames),args.locator);
   const state=args.state??'visible';
   if(!['visible','hidden','attached','detached'].includes(state))throw Error('Unsupported element state');
   await locator.waitFor({state,timeout});return {tab_id:id,state};
  }
  if(args.action==='wait_url'){await page.waitForURL(args.url,{timeout,waitUntil:'domcontentloaded'});return {tab_id:id,url:page.url()};}
  if(args.action==='wait_load'){const state=args.state??'load';if(!['load','domcontentloaded','networkidle'].includes(state))throw Error('Unsupported load state');await page.waitForLoadState(state,{timeout});return {tab_id:id,state};}
  if(args.action==='read'){const textOffset=args.text_offset??0;if(!Number.isInteger(textOffset)||textOffset<0)throw Error('Invalid text_offset');return {...await snapshot(page,pageRange(args),textOffset),tab_id:id};}
  if(args.action==='inspect'){
   const item=buildLocator(locatorRoot(page,args.frames),args.locator);
   const count=await item.count();
   if(args.property==='count')return {tab_id:id,value:count};
   if(count!==1)throw Error('Inspection requires exactly one matching element');
   if(args.property==='visible')return {tab_id:id,value:await item.isVisible()};
   if(args.property==='enabled')return {tab_id:id,value:await item.isEnabled()};
   if(args.property==='text')return {tab_id:id,value:(await item.innerText()).slice(0,20000)};
   if(args.property==='attribute'){
    if(!/^(id|class|role|title|alt|href|src|name|type|placeholder|disabled|checked|aria-[a-z-]+|data-testid)$/.test(args.attribute||''))throw Error('Attribute is not available for inspection');
    return {tab_id:id,value:(await item.getAttribute(args.attribute))?.slice(0,20000)??null};
   }
   throw Error('Unknown inspection property');
  }
  if(args.action==='find')return {tab_id:id,...await findElements(page,args)};
  let target;
  const targetActions=['click','double_click','type','append','type_sequential','select','press_key','set_checked','click_navigation'];
  if(targetActions.includes(args.action)&&args.locator){
   const locator=buildLocator(locatorRoot(page,args.frames),args.locator);
   target=await locator.elementHandle();if(!target)throw Error('Element not found');
   await assertPublicTarget(target);
  } else if(targetActions.includes(args.action)) {
   const current=await snapshot(page);
   if(!args.snapshot||args.snapshot!==current.snapshot)throw Error('Page changed. Read the page again before acting.');
   if(args.action==='press_key') {
    if(!/^(Enter|Tab|Escape|Backspace|Delete|ArrowUp|ArrowDown|ArrowLeft|ArrowRight|Home|End|PageUp|PageDown|Space)$/.test(args.key)||current.focused_sensitive)throw Error('Use a private login handoff for passwords or verification codes.');
   } else {
    const control=current.controls.find(c=>c.ref===args.ref);
    if(!control)throw Error('Read the page and use a current control reference.');
    if(['type','append','type_sequential','select','set_checked'].includes(args.action)&&control.sensitive)throw Error('Use a private login handoff for passwords or verification codes.');
    const handle=await page.evaluateHandle(ref=>window.__ovBrowserRefs.nodes.get(ref),args.ref);
    target=handle.asElement();if(!target)throw Error('Control changed. Read the page again.');
   }
  }
  switch(args.action){
   case 'verify_access':
   case 'navigate': {const url=new URL(args.url);if(!['http:','https:'].includes(url.protocol)||url.username||url.password)throw Error('HTTP URL required');const response=await page.goto(url.href,{waitUntil:'domcontentloaded',timeout:20000});if(args.action==='verify_access'&&(!response||!response.ok()))throw Error('Page inaccessible');break;}
   case 'select_tab':
   case 'read':break;
   case 'reload':await page.reload({waitUntil:'domcontentloaded'});break;
   case 'back':await page.goBack({waitUntil:'domcontentloaded'});break;
   case 'forward':await page.goForward({waitUntil:'domcontentloaded'});break;
   case 'click_navigation':{
    const waiting=args.url?page.waitForURL(args.url,{timeout,waitUntil:'domcontentloaded'}):page.waitForNavigation({timeout,waitUntil:'domcontentloaded'});
    await Promise.all([waiting,target.click({button:args.button,modifiers:args.modifiers,force:args.force})]);break;
   }
   case 'click':await target.click({button:args.button,modifiers:args.modifiers,force:args.force});break;
   case 'double_click':await target.dblclick({button:args.button,modifiers:args.modifiers,force:args.force});break;
   case 'append':
   case 'type_sequential':await target.type(args.text);break;
   case 'set_checked':if(typeof args.checked!=='boolean')throw Error('checked must be a boolean');await target.setChecked(args.checked,{force:args.force});break;
   case 'type':await target.fill(args.text);break;
   case 'select':await target.selectOption(args.options??args.value);break;
   case 'press_key':if(target)await target.press(args.key);else await page.keyboard.press(args.key);break;
   case 'scroll':await page.mouse.wheel(0,Math.max(-2000,Math.min(2000,Number(args.amount)||600)));break;
   default:throw Error('Unsupported browser action');
  }
  const result=await snapshot(page);
  if(args.action==='verify_access'){
   if(new URL(page.url()).origin!==new URL(args.url).origin||result.controls.some(c=>c.sensitive))throw Error('Access not verified');
   if(typeof args.expected_text==='string'&&args.expected_text.trim()&&result.text.includes(args.expected_text))return {verified:true};
   return {verified:false,url:page.url(),screenshot:(await page.screenshot({type:'png'})).toString('base64')};
  }
  return {...result,tab_id:id};
 } finally {await browser.close();}
}
function browserError(error){
 const text=String(error?.message??'');
 const code=/strict mode violation/i.test(text)?'multiple_matches':/Timeout|timed out/i.test(text)?'timeout':/private login/i.test(text)?'private_login_required':/changed|detached/i.test(text)?'page_changed':/not found|No node/i.test(text)?'not_found':'invalid_request';
 // Playwright errors can contain page data or typed values; never echo raw traces.
 const messages={multiple_matches:'More than one element matched. Narrow the locator.',timeout:'The action timed out. Check whether the element exists, is visible, enabled, and unobstructed.',private_login_required:'Use a private login handoff for credential fields.',page_changed:'The page or element changed. Read again or use a locator.',not_found:'The tab or element was not found. Refresh the tab list or locator.',invalid_request:'Invalid browser request. Check the action and arguments.'};
 return {code,message:messages[code]};
}
module.exports={run,browserError};
if(require.main===module)(async()=>{let raw='';for await(const chunk of process.stdin)raw+=chunk;process.stdout.write(JSON.stringify(await run(JSON.parse(raw))));})().catch(error=>{process.stdout.write(JSON.stringify({error:browserError(error)}));});
