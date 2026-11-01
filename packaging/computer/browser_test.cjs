// Run inside the disposable computer image: node --test /tmp/browser_test.cjs
const {test}=require('node:test');
const assert=require('node:assert/strict');
const http=require('node:http');
const {chromium}=require('/opt/browser/node_modules/playwright');
const {run}=require('/usr/local/lib/openvibely-browser.cjs');
test('browser references, credential fields and login evidence',async()=>{
 const server=http.createServer((req,res)=>{res.setHeader('Content-Type','text/html');res.end('<h1>Your workspace</h1><button id="save" onclick="this.textContent=\'Saved\'">Save</button><input id="normal"><label for="otp">Verification code</label><input id="otp"><input id="password" type="password"><input id="autofill" autocomplete="one-time-code">');});
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 const url='http://127.0.0.1:'+server.address().port;
 const browser=await chromium.connectOverCDP('http://127.0.0.1:'+process.env.OPENVIBELY_CDP_PORT);
 try{
  let state=await run({action:'navigate',url});
  const ref=id=>state.controls.find(c=>c.id===id).ref;
  state=await run({action:'type',snapshot:state.snapshot,ref:ref('normal'),text:'hello'});
  for(const id of ['password','otp','autofill'])await assert.rejects(run({action:'type',snapshot:state.snapshot,ref:ref(id),text:'secret'}),/private login/);
  await assert.rejects(run({action:'click',selector:'#save'}),/Read|read/);
  const page=browser.contexts()[0].pages()[0];
  const old=state;
  await page.evaluate(()=>document.querySelector('#save').outerHTML='<button id="save">Save</button>');
  await assert.rejects(run({action:'click',snapshot:old.snapshot,ref:old.controls.find(c=>c.id==='save').ref}),/changed/);
  state=await run({action:'read'});
  await page.locator('#password').focus();
  state=await run({action:'read'});
  await assert.rejects(run({action:'press_key',snapshot:state.snapshot,key:'Enter'}),/private login/);
  await assert.rejects(run({action:'verify_access',url,expected_text:'Your workspace'}),/not verified/);
  // Remove the login fields at the HTTP source, so navigation sees a signed-in page.
  server.removeAllListeners('request');server.on('request',(req,res)=>{res.setHeader('Content-Type','text/html');res.end('<h1>Your workspace</h1><p>Welcome, signed-in user</p><button>Sign out</button>');});
  const exact=await run({action:'verify_access',url,expected_text:'Your workspace'});assert.equal(exact.verified,true);
  const visual=await run({action:'verify_access',url,expected_text:'Account dashboard'});assert.equal(visual.verified,false);assert.ok(Buffer.from(visual.screenshot,'base64').length>100);
  state=await run({action:'read'});
  await run({action:'click',snapshot:state.snapshot,ref:state.controls[0].ref});
 }finally{await browser.close();await new Promise(resolve=>server.close(resolve));}
});
