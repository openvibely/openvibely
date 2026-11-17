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
 const client=await chromium.connectOverCDP('http://127.0.0.1:19222');
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
  await t.test('locators, filters and nested frames',async()=>{
   const state=await run({action:'read'}),tab_id=state.tab_id;
   const page=client.contexts()[0].pages().find(p=>p.url().startsWith(url));
   await page.setContent('<button>Save</button><button>Save copy</button><label>Email<input></label><input placeholder="Search"><p data-testid="result">Found</p><iframe srcdoc="&lt;button&gt;Inside&lt;/button&gt;"></iframe>');
   assert.equal((await run({action:'find',tab_id,locator:{role:'button',name:'Save',exact:true}})).count,1);
   assert.equal((await run({action:'find',tab_id,locator:{label:'Email'}})).count,1);
   assert.equal((await run({action:'find',tab_id,locator:{placeholder:'Search'}})).count,1);
   assert.equal((await run({action:'find',tab_id,locator:{test_id:'result'}})).elements[0].text,'Found');
   assert.equal((await run({action:'find',tab_id,locator:{role:'button',has_not_text:'copy'}})).count,1);
   assert.equal((await run({action:'find',tab_id,frames:['iframe'],locator:{text:'Inside'}})).count,1);
   const result=await run({action:'find',tab_id,locator:{role:'button'},limit:1});assert.equal(result.next_offset,1);
   await page.locator('button').first().evaluate(n=>n.outerHTML='<button>Save</button>');
   assert.equal((await run({action:'find',tab_id,locator:{role:'button',name:'Save',exact:true}})).count,1);
  });
  await t.test('element inspection and paginated reads',async()=>{
   const page=client.contexts()[0].pages()[0];
   await page.setContent('<input type="password" value="secret"><button disabled title="Public">Disabled</button>'+Array.from({length:130},(_,i)=>`<button>${i}</button>`).join('')+'<p>'+ 'x'.repeat(21000)+'</p>');
   assert.equal((await run({action:'inspect',locator:{role:'button',name:'Disabled'},property:'enabled'})).value,false);
   assert.equal((await run({action:'inspect',locator:{role:'button',name:'Disabled'},property:'attribute',attribute:'title'})).value,'Public');
   await assert.rejects(run({action:'inspect',locator:{selector:'input'},property:'attribute',attribute:'value'}),/not available/);
   assert.equal((await run({action:'inspect',locator:{role:'button'},property:'count'})).value,131);
   assert.ok((await run({action:'read',offset:100})).controls.length>0);
   assert.ok((await run({action:'read',text_offset:20000})).text.length>1000);
  });
 }finally{await client.close();await browser.close();await new Promise(resolve=>server.close(resolve));process.env.HOME=previousHome;await fs.rm(home,{recursive:true,force:true});}
});
