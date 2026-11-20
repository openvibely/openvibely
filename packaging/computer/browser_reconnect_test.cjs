// Opt-in: run inside a disposable computer image with this directory mounted.
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs/promises');
const os=require('node:os');
const path=require('node:path');
const http=require('node:http');
const {chromium}=require('/opt/browser/node_modules/playwright');
const {createReconnectingRunner,browserError}=require('./browser.cjs');
test('worker reconnects after browser replacement without replaying interrupted actions',{timeout:60000},async()=>{
 const oldHome=process.env.HOME;
 const home=await fs.mkdtemp(path.join(os.tmpdir(),'browser-reconnect-'));
 process.env.HOME=home;process.env.OPENVIBELY_CDP_PORT='19223';
 const executablePath=(await fs.readFile('/usr/local/lib/openvibely-chromium-path','utf8')).trim();
 const launch=()=>chromium.launch({headless:true,executablePath,args:['--no-sandbox','--remote-debugging-port=19223']});
 let browser;const runner=createReconnectingRunner();
 let requests=0,received;
 const arrival=new Promise(resolve=>received=resolve);
 const server=http.createServer((req,res)=>{requests++;received();});
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 try{
  browser=await launch();
  const first=await runner.run({action:'open_tab'});
  const second=await runner.run({action:'open_tab'});
  await runner.run({action:'select_tab',tab_id:second.tab_id});
  assert.equal((await runner.run({action:'read'})).tab_id,second.tab_id);
  assert.equal((await runner.run({action:'tabs'})).tabs.length,2);
  await browser.close();
  await assert.rejects(runner.run({action:'tabs'}),error=>browserError(error).code==='browser_unavailable');
  browser=await launch();
  const context=await browser.newContext();
  await context.newPage();await context.newPage();
  const tabs=await runner.run({action:'tabs'});
  assert.equal(tabs.tabs.length,2);
  assert.ok(tabs.tabs.every(tab=>tab.tab_id!==first.tab_id&&tab.tab_id!==second.tab_id));
  // The selected tab from the old process no longer exists: use an available tab.
  assert.equal((await runner.run({action:'read'})).tab_id,tabs.tabs[0].tab_id);
  await runner.run({action:'select_tab',tab_id:tabs.tabs[1].tab_id});
  assert.equal((await runner.run({action:'read'})).tab_id,tabs.tabs[1].tab_id);
  const interrupted=runner.run({action:'navigate',url:'http://127.0.0.1:'+server.address().port});
  const rejected=assert.rejects(interrupted,error=>['browser_unavailable','page_changed'].includes(browserError(error).code));
  rejected.catch(()=>{});
  await arrival;await browser.close();await rejected;
  browser=await launch();await (await browser.newContext()).newPage();
  assert.ok((await runner.run({action:'tabs'})).tabs.length);
  assert.equal(requests,1,'interrupted navigation must not replay');
 }finally{
  await runner.close();if(browser)await browser.close();server.closeAllConnections();await new Promise(resolve=>server.close(resolve));
  process.env.HOME=oldHome;await fs.rm(home,{recursive:true,force:true});
 }
});
