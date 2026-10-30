package pages

import "testing"

func TestSpreadsheetSessionTracksConcurrentTextTimeouts(t *testing.T) {
	runSpreadsheetWorkerNodeTest(t, `
(async()=>{
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const source=fs.readFileSync('../../assets/files/file-sheets.js','utf8');
const reader=source.slice(source.indexOf('function read('),source.indexOf(' async function render('));
function harness(){
 let now=0,nextID=0,worker;const timers=new Map(),errors=[];
 const context={Worker:class{constructor(){worker=this;this.terminated=false}postMessage(){}terminate(){this.terminated=true}},setTimeout(callback,delay){const id=++nextID;timers.set(id,{at:now+delay,callback});return id},clearTimeout(id){timers.delete(id)}};
 vm.createContext(context);vm.runInContext(reader,context);
 const emit=data=>worker.onmessage({data});
 const advance=duration=>{const until=now+duration;while(true){const next=[...timers].filter(([,timer])=>timer.at<=until).sort((a,b)=>a[1].at-b[1].at)[0];if(!next)break;now=next[1].at;timers.delete(next[0]);next[1].callback()}now=until};
 return {timers,errors,emit,advance,get worker(){return worker},async open(catalogPending=false){const opening=context.read(new ArrayBuffer(0),'xlsx');emit({type:'ready',index:0,names:['First','Second','Third'],sheet:{name:'First',valuesPending:true},updatesPending:true,catalogPending});const session=await opening;session.onUpdateError=(index,error)=>errors.push({index,message:error.message});return session}};
}
for(const finishedIndex of [0,1])for(const type of ['update','update-error']){
 const h=harness(),session=await h.open();
 h.advance(5000);
 const loading=session.load(1);
 // Sheet responses may omit index; the originating request identifies the tab.
 h.emit({type:'sheet',requestId:1,sheet:{name:'Second',valuesPending:true},updatesPending:true});await loading;
 h.advance(1000);
 h.emit({type,index:finishedIndex,sheet:{name:'Finished'},updatesPending:false,error:'Text failed'});
 assert.equal(session.updatesPending,true,'another tab is still resolving');
 const stalled=session.load(2);const rejection=assert.rejects(stalled,/Spreadsheet took too long to read/);
 const remaining=finishedIndex===0?14000:9000;
 h.advance(remaining-1);assert.equal(h.worker.terminated,false,'the outstanding tab retains its own deadline');
 h.advance(1);assert.equal(h.worker.terminated,true,'the outstanding tab must time out');
 await rejection;
 assert.equal(h.errors.at(-1).message,'Spreadsheet took too long to read');
 assert.equal(h.timers.size,0,'timeout clears every request and update timer');
 await assert.rejects(session.load(0),/worker is closed/);
}
{
 const h=harness(),session=await h.open();
 h.advance(5000);const loading=session.load(1);
 h.emit({type:'sheet',index:1,requestId:1,sheet:{name:'Second'},updatesPending:true});await loading;
 h.advance(5000);h.emit({type:'update',index:1,updatesPending:true,sheet:{name:'Second'}});
 h.advance(5000);
 assert.equal(h.worker.terminated,true,'progress in another tab must not extend the first tab deadline');
 assert.equal(h.timers.size,0);
}
{
 const h=harness(),session=await h.open(true);
 const loading=session.load(1);h.emit({type:'sheet',requestId:1,sheet:{name:'Second'},updatesPending:true});await loading;
 h.emit({type:'update',index:0,updatesPending:false,sheet:{name:'First'}});
 h.emit({type:'update',index:1,updatesPending:false,sheet:{name:'Second'}});
 assert.equal(session.updatesPending,false);
 assert.equal(h.timers.size,1,'the catalog keeps its independent timeout');
 h.emit({type:'catalog',names:['First','Second']});assert.equal(h.timers.size,0);
 h.advance(20000);assert.equal(h.worker.terminated,false);assert.equal(h.errors.length,0);
 session.dispose();assert.equal(h.worker.terminated,true);
}
{
 const h=harness(),session=await h.open();
 const loading=session.load(1);h.emit({type:'sheet',requestId:1,sheet:{name:'Second'},updatesPending:true});await loading;
 session.dispose();assert.equal(h.timers.size,0);
 h.advance(20000);assert.equal(h.errors.length,0,'disposed sessions must not report stale timeouts');
}
})().catch(error=>{console.error(error);process.exitCode=1});
`)
}

func TestSpreadsheetSessionProtectsOnDemandODSDimensionScan(t *testing.T) {
	runSpreadsheetWorkerNodeTest(t, `
(async()=>{
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm'),XLSX=require('../../assets/file-vendor/xlsx.full.min.js');
const workbook=XLSX.utils.book_new();
XLSX.utils.book_append_sheet(workbook,XLSX.utils.aoa_to_sheet([['First value']]),'First');
XLSX.utils.book_append_sheet(workbook,XLSX.utils.aoa_to_sheet(Array.from({length:12000},(_,row)=>[row])),'Second');
const messages=[];let resolve,reject;
const catalog=new Promise((yes,no)=>{resolve=yes;reject=no});
const deadline=setTimeout(()=>reject(Error('ODS catalog timed out')),10000);
const workerContext={XLSX,TextDecoder,ReadableStream,DecompressionStream,setTimeout,importScripts(){},close(){},postMessage(message){messages.push(message);if(message.type==='catalog')resolve();if(message.type==='error')reject(Error(message.error))}};
vm.createContext(workerContext);vm.runInContext(fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8'),workerContext);
try{
 workerContext.onmessage({data:{buffer:XLSX.write(workbook,{type:'array',bookType:'ods',compression:true}),format:'ods'}});
 await catalog;
}finally{clearTimeout(deadline)}
await workerContext.loadODSZipSheet(1,1);
const ready=messages.find(message=>message.type==='ready');
const names=messages.find(message=>message.type==='catalog');
const partial=messages.find(message=>message.type==='sheet'&&message.index===1);
const complete=messages.find(message=>message.type==='update'&&message.index===1&&!message.sheet.dimensionsPending);
assert.equal(partial.sheet.dimensionsPending,true);
assert.equal(partial.updatesPending,true,'on-demand dimension scans must retain timeout protection');
assert.equal(partial.catalogPending,false,'the already-known catalog cannot protect this scan');
assert.equal(complete.updatesPending,false,'final dimensions must clear the pending update state');
assert.equal(complete.sheet.totalRows,12000);
const source=fs.readFileSync('../../assets/files/file-sheets.js','utf8');
const reader=source.slice(source.indexOf('function read('),source.indexOf(' async function render('));
for(const outcome of ['complete','timeout','dispose']){
 let now=0,nextID=0,worker;const timers=new Map(),errors=[],updates=[];
 const context={Worker:class{constructor(){worker=this;this.terminated=false}postMessage(){}terminate(){this.terminated=true}},setTimeout(callback,delay){const id=++nextID;timers.set(id,{at:now+delay,callback});return id},clearTimeout(id){timers.delete(id)}};
 vm.createContext(context);vm.runInContext(reader,context);
 const advance=duration=>{const until=now+duration;while(true){const next=[...timers].filter(([,timer])=>timer.at<=until).sort((a,b)=>a[1].at-b[1].at)[0];if(!next)break;now=next[1].at;timers.delete(next[0]);next[1].callback()}now=until};
 const opening=context.read(new ArrayBuffer(0),'ods');worker.onmessage({data:ready});
 const session=await opening;session.onUpdateError=(index,error)=>errors.push(error.message);session.onUpdate=(index,sheet)=>updates.push(sheet);
 worker.onmessage({data:names});assert.equal(timers.size,0,'initial loading is complete');
 const loading=session.load(1);worker.onmessage({data:partial});const sheet=await loading;
 assert.equal(sheet.dimensionsPending,true);assert.equal(session.updatesPending,true);assert.equal(timers.size,1);
 if(outcome==='complete'){
  worker.onmessage({data:complete});
  assert.equal(session.updatesPending,false);assert.equal(timers.size,0);assert.equal(updates[0].dimensionsPending,false);
  advance(20000);assert.equal(worker.terminated,false);assert.equal(errors.length,0);session.dispose();
 }else if(outcome==='timeout'){
  advance(14999);assert.equal(worker.terminated,false);advance(1);
  assert.equal(worker.terminated,true);assert.deepEqual(errors,['Spreadsheet took too long to read']);assert.equal(timers.size,0);
  await assert.rejects(session.load(1),/worker is closed/);
  // Retry creates a fresh working session after the stalled worker is cleaned up.
  const retry=context.read(new ArrayBuffer(0),'ods');worker.onmessage({data:ready});const restarted=await retry;
  worker.onmessage({data:names});assert.equal(restarted.first.rows[0][0],'First value');assert.equal(worker.terminated,false);restarted.dispose();
 }else{
  session.dispose();assert.equal(worker.terminated,true);assert.equal(timers.size,0);advance(20000);assert.equal(errors.length,0);
 }
}
})().catch(error=>{console.error(error);process.exitCode=1});
`)
}
