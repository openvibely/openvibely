// Isolated real-Chromium integration tests. Run in the computer image:
// docker run --rm --network none --entrypoint node -v "$PWD/packaging/computer:/suite:ro" IMAGE --test /suite/browser_features_test.cjs
const {test}=require('node:test');
const assert=require('node:assert/strict');
const http=require('node:http');
const fs=require('node:fs/promises');
const os=require('node:os');
const path=require('node:path');
const {chromium}=require('/opt/browser/node_modules/playwright');
const {run,browserError,createRunner}=require('./browser.cjs');
test('project browser features in isolated Chromium',{timeout:30000},async t=>{
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
  await t.test('element actions and credential restrictions',async()=>{
   const page=client.contexts()[0].pages()[0];
   await page.setContent('<input id="normal"><input id="password" type="password"><input type="checkbox"><select multiple><option value="a">Alpha</option><option value="b">Beta</option></select><button ondblclick="this.textContent=\'Twice\'">Double</button>');
   await run({action:'type',locator:{selector:'#normal'},text:'a'});
   await run({action:'append',locator:{selector:'#normal'},text:'b'});
   await run({action:'type_sequential',locator:{selector:'#normal'},text:'c'});
   assert.equal(await page.locator('#normal').inputValue(),'abc');
   await run({action:'press_key',locator:{selector:'#normal'},key:'Backspace'});
   assert.equal(await page.locator('#normal').inputValue(),'ab');
   await run({action:'set_checked',locator:{role:'checkbox'},checked:true});assert.equal(await page.getByRole('checkbox').isChecked(),true);
   await run({action:'select',locator:{selector:'select'},options:[{label:'Alpha'},{index:1}]});assert.deepEqual(await page.locator('select').evaluate(n=>[...n.selectedOptions].map(o=>o.value)),['a','b']);
   await run({action:'double_click',locator:{role:'button'}});assert.equal(await page.getByRole('button').innerText(),'Twice');
   for(const action of ['type','append','type_sequential','press_key'])await assert.rejects(run({action,locator:{selector:'#password'},text:'secret',key:'a'}),/private login/);
   const old=await run({action:'read'});await page.locator('#normal').evaluate(n=>n.outerHTML='<input id="normal">');
   await assert.rejects(run({action:'type',snapshot:old.snapshot,ref:old.controls.find(c=>c.id==='normal').ref,text:'stale'}),/changed/);
   await run({action:'type',locator:{selector:'#normal'},text:'fresh'});assert.equal(await page.locator('#normal').inputValue(),'fresh');
  });
  await t.test('waits, navigation and safe diagnostics',async()=>{
   const page=client.contexts()[0].pages()[0];
   await page.setContent('<button>Duplicate</button><button>Duplicate</button><a href="'+url+'">Go</a>');
   await page.evaluate(()=>setTimeout(()=>{const b=document.createElement('button');b.id='later';b.textContent='Ready';document.body.append(b)},100));
   await run({action:'wait_element',locator:{selector:'#later'},state:'visible',timeout_ms:2000});
   try{await run({action:'click',locator:{role:'button',name:'Duplicate'},timeout_ms:200});assert.fail('must reject ambiguous locator');}catch(e){assert.equal(browserError(e).code,'multiple_matches');}
   try{await run({action:'wait_element',locator:{text:'secret-not-in-error'},timeout_ms:100});assert.fail('must time out');}catch(e){assert.equal(browserError(e).code,'timeout');assert.ok(!JSON.stringify(browserError(e)).includes('secret-not-in-error'));}
   await run({action:'click_navigation',locator:{role:'link',name:'Go'},url:url+'/',timeout_ms:2000});
   await run({action:'wait_url',url:url+'/'});await run({action:'wait_load',state:'load'});
  });
  await t.test('browser viewport, full-page and cropped screenshots',async()=>{
   const page=client.contexts()[0].pages()[0];await page.setContent('<div style="height:1800px;background:linear-gradient(red,blue)">Tall</div>');
   const image=async args=>Buffer.from((await run({action:'screenshot',...args})).content[0].data,'base64');
   const viewport=await image({}),full=await image({full_page:true}),clip=await image({clip:{x:0,y:0,width:80,height:80}});
   assert.equal(viewport[0],255);assert.equal(viewport[1],216);assert.ok(full.length>viewport.length);assert.ok(clip.length<viewport.length);
   await assert.rejects(run({action:'screenshot',clip:{x:0,y:0,width:-1,height:2}}),/Invalid/);
  });
  await t.test('website uploads and downloads use computer Files',async()=>{
   server.removeAllListeners('request');server.on('request',(req,res)=>{if(req.url==='/file'){res.setHeader('Content-Disposition','attachment; filename="result.txt"');res.end('downloaded');}else if(req.url==='/media'){res.setHeader('Content-Type','text/plain');res.end('media bytes');}else{res.setHeader('Content-Type','text/html');res.end('<input type="file" multiple><a href="/file">Download</a><a href="/media">Media</a>');}});
   await fs.mkdir(path.join(home,'Files'),{recursive:true});await fs.writeFile(path.join(home,'Files','input.txt'),'upload data');await fs.writeFile(path.join(home,'outside.txt'),'private');await fs.symlink(path.join(home,'outside.txt'),path.join(home,'Files','escape'));
   await run({action:'navigate',url});
   await run({action:'upload',locator:{selector:'input'},files:['input.txt']});
   assert.equal(await client.contexts()[0].pages()[0].locator('input').evaluate(n=>n.files[0].name),'input.txt');
   await assert.rejects(run({action:'upload',locator:{selector:'input'},files:['../outside.txt']}),/inside computer Files/);
   await assert.rejects(run({action:'upload',locator:{selector:'input'},files:['escape']}),/inside computer Files/);
   const saved=await run({action:'download',locator:{text:'Download',exact:true}});assert.equal(await fs.readFile(path.join(home,'Files',saved.computer_path),'utf8'),'downloaded');
   const media=await run({action:'download_media',locator:{text:'Media',exact:true}});assert.equal(await fs.readFile(path.join(home,'Files',media.computer_path),'utf8'),'media bytes');
   const again=await run({action:'download',locator:{text:'Download',exact:true}});assert.notEqual(saved.computer_path,again.computer_path);
  });
  await t.test('console diagnostics do not collect idle human browsing',async()=>{
   server.removeAllListeners('request');server.on('request',(req,res)=>{res.setHeader('Content-Type','text/html');res.end('<script>console.log("agent navigation");console.error("fixture error")</script><h1>Logs</h1>');});
   const execute=createRunner(client);
   await execute({action:'navigate',url});
   await client.contexts()[0].pages()[0].evaluate(()=>console.log('idle human secret'));
   const result=await execute({action:'logs'});assert.ok(result.entries.some(x=>x.text==='agent navigation'));assert.ok(result.entries.some(x=>x.type==='error'));assert.ok(!JSON.stringify(result).includes('idle human secret'));
  });
  await t.test('dialogs survive calls and can be inspected and resolved',async()=>{
   const page=client.contexts()[0].pages()[0];await page.setContent('<button onclick="document.querySelector(\'p\').textContent=prompt(\'Name?\',\'Default\')">Ask</button><p></p>');
   const execute=createRunner(client);
   const clicked=await execute({action:'click',locator:{role:'button'}});assert.equal(clicked.dialog_pending,true);
   const inspected=await execute({action:'dialog',dialog_action:'inspect'});assert.equal(inspected.dialog.type,'prompt');assert.equal(inspected.dialog.message,'Name?');
   await execute({action:'dialog',dialog_action:'accept',text:'Ada'});assert.equal(await page.locator('p').innerText(),'Ada');
   await execute({action:'click',locator:{role:'button'}});await execute({action:'dialog',dialog_action:'dismiss'});assert.equal((await execute({action:'dialog'})).dialog,null);
  });
 }finally{await client.close();await browser.close();await new Promise(resolve=>server.close(resolve));process.env.HOME=previousHome;await fs.rm(home,{recursive:true,force:true});}
});
