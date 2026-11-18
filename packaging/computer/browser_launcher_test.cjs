// Opt-in check; Docker --init is needed for xvfb-run's startup signal handling:
// docker run --rm --init --network none --entrypoint xvfb-run -v "$PWD/packaging/computer:/suite:ro" IMAGE --server-num=100 node --test /suite/browser_launcher_test.cjs
const {test}=require('node:test');
const assert=require('node:assert/strict');
const {spawn,execFile}=require('node:child_process');
const {promisify}=require('node:util');
const fs=require('node:fs/promises');
const {chromium}=require('/opt/browser/node_modules/playwright');
const exec=promisify(execFile);

test('desktop launcher reuses the task profile and restores tabs without adding blanks',{timeout:60000},async()=>{
 const base='/home/ubuntu/.desktops/task-launcher-fixture';
 await fs.mkdir(base+'/home',{recursive:true});
 const server=require('node:http').createServer((req,res)=>res.end('Launcher fixture'));
 await new Promise(r=>server.listen(0,'127.0.0.1',r));
 const url='http://127.0.0.1:'+server.address().port;
 const env={...process.env,HOME:base+'/home',XDG_CONFIG_HOME:base+'/config',XDG_CACHE_HOME:base+'/cache'};
 const launch=()=>spawn('/usr/local/bin/openvibely-browser',['--restore-last-session'],{env,stdio:'ignore'});
 let child,browser;
 async function connect(){
  for(let n=0;n<100;n++){
   try{return await chromium.connectOverCDP('http://127.0.0.1:10001',{timeout:500});}catch{}
   await new Promise(r=>setTimeout(r,100));
  }
  throw Error('Browser did not start');
 }
 async function stop(){
  const exited=new Promise(resolve=>child.once('exit',resolve));
  const session=await browser.newBrowserCDPSession();
  await session.send('Browser.close').catch(()=>{});
  await exited;
  await browser.close();browser=null;child=null;
 }
 try{
  child=launch();browser=await connect();
  const context=browser.contexts()[0];
  await context.pages()[0].goto(url+'/saved');
  await context.addCookies([{name:'fixture',value:'retained',domain:'example.test',path:'/',expires:Math.floor(Date.now()/1000)+3600}]);
  await exec('exo-open',['--launch','WebBrowser',url+'/launcher'],{env,timeout:10000});
  for(let n=0;n<100&&!context.pages().some(p=>p.url()===url+'/launcher');n++)await new Promise(r=>setTimeout(r,50));
  assert.ok(context.pages().some(p=>p.url()===url+'/launcher'),'desktop shortcut did not use the task browser: '+JSON.stringify(context.pages().map(p=>p.url())));
  const before=context.pages().map(p=>p.url()).sort();
  assert.ok(before.includes(url+'/saved'));
  await stop();
  child=launch();browser=await connect();
  for(let n=0;n<100&&browser.contexts()[0].pages().length<before.length;n++)await new Promise(r=>setTimeout(r,50));
  assert.deepEqual(browser.contexts()[0].pages().map(p=>p.url()).sort(),before);
  assert.equal((await browser.contexts()[0].cookies('https://example.test')).find(c=>c.name==='fixture')?.value,'retained');
 }finally{
  if(browser&&child)await stop();
  child?.kill('SIGKILL');
  await fs.rm(base,{recursive:true,force:true});
  await new Promise(r=>server.close(r));
 }
});
