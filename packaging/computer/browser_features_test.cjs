// Isolated real-Chromium integration tests. Run in the computer image:
// docker run --rm --network none --entrypoint node -v "$PWD/packaging/computer:/suite:ro" IMAGE --test /suite/browser_features_test.cjs
const {test}=require('node:test');
const assert=require('node:assert/strict');
const http=require('node:http');
const fs=require('node:fs/promises');
const os=require('node:os');
const path=require('node:path');
const {chromium}=require('/opt/browser/node_modules/playwright');
const {run}=require('./browser.cjs');
test('project browser features in isolated Chromium',async t=>{
 const home=await fs.mkdtemp(path.join(os.tmpdir(),'ov-browser-test-'));
 const previousHome=process.env.HOME;process.env.HOME=home;process.env.OPENVIBELY_CDP_PORT='19222';
 const browser=await chromium.launch({headless:true,executablePath:chromium.executablePath(),args:['--no-sandbox','--remote-debugging-port=19222']});
 const server=http.createServer((req,res)=>{res.setHeader('Content-Type','text/html');res.end('<h1>Fixture</h1><button id="save" onclick="this.textContent=\'Saved\'">Save</button><input id="normal"><input type="password" id="password">');});
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 const url='http://127.0.0.1:'+server.address().port;
 try{
  await t.test('tabs remain identifiable across tool calls',async()=>{
   const first=await run({action:'open_tab'}),second=await run({action:'open_tab'});
   assert.notEqual(first.tab_id,second.tab_id);
   await run({action:'navigate',tab_id:first.tab_id,url});
   assert.equal((await run({action:'read',tab_id:first.tab_id})).title,'');
   assert.equal((await run({action:'read'})).tab_id,second.tab_id);
   await run({action:'select_tab',tab_id:first.tab_id});
   assert.equal((await run({action:'read'})).tab_id,first.tab_id);
   assert.ok((await run({action:'tabs'})).tabs.some(x=>x.tab_id===second.tab_id));
   await run({action:'reload',tab_id:first.tab_id});
   await run({action:'close_tab',tab_id:second.tab_id});
   await assert.rejects(run({action:'read',tab_id:second.tab_id}),/Tab not found/);
  });
 }finally{await browser.close();await new Promise(resolve=>server.close(resolve));process.env.HOME=previousHome;await fs.rm(home,{recursive:true,force:true});}
});
