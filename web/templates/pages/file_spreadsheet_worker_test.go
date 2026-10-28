package pages

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestSpreadsheetWorkerBoundsSparsePreview(t *testing.T) {
	runSpreadsheetWorkerNodeTest(t, `
const assert = require('node:assert/strict'), fs = require('node:fs'), vm = require('node:vm');
const XLSX = require('../../assets/file-vendor/xlsx.full.min.js');
const names = ['Wide0', 'Wide1', 'Wide2', 'Empty'];
const sheets = names.map((name, index) => index < 3
  ? {'!ref':'A1:XFD10001', '!fullref':'A1:XFD10001', B4:{t:'s',v:'kept'}, XFD10001:{t:'s',v:'far corner'}}
  : {});
const calls = [];
XLSX.read = (_input, options) => {
 calls.push(options);
 const indexes = typeof options.sheets === 'number' ? [options.sheets] : options.sheets;
 const parsed = {};
 for (const index of indexes || []) parsed[names[index]] = sheets[index];
 return {SheetNames:names, Sheets:parsed};
};
const original = XLSX.utils.sheet_to_json;
let allocated = 0;
XLSX.utils.sheet_to_json = function(sheet, options) {
 const range = options.range, rows = range.e.r-range.s.r+1, columns = range.e.c-range.s.c+1;
 assert.equal(range.s.r,0); assert.equal(range.s.c,0);
 assert.ok(columns <= 100, 'column cap must apply before row expansion');
 assert.ok(rows * columns <= 100000, 'per-sheet cell cap must apply before row expansion');
 allocated += rows * columns;
 assert.ok(allocated <= 200000, 'workbook preview allocation must be bounded');
 return original(sheet, options);
};
let output;
const context = {XLSX, importScripts(){}, postMessage(value){output=value}, close(){}};
vm.createContext(context);
vm.runInContext(fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8'), context);
context.onmessage({data:{buffer:new ArrayBuffer(0),format:'xlsx'}});
assert.equal(output.type,'ready');
assert.deepEqual(Array.from(output.names),names);
assert.equal(output.sheet.totalRows,10001); assert.equal(output.sheet.totalColumns,16384);
assert.equal(output.sheet.rows.length,1000); assert.equal(output.sheet.truncated,true);
assert.equal(output.sheet.rows[3][1],'kept');
assert.equal(output.sheet.rows[2][1],'');
assert.deepEqual(calls.map(call => [call.bookSheets||false,Array.isArray(call.sheets)?Array.from(call.sheets):call.sheets,call.sheetRows]), [[true,undefined,undefined],[false,0,1],[false,[0],1000]]);

context.onmessage({data:{type:'load',index:1,requestId:7}});
assert.equal(output.type,'sheet'); assert.equal(output.requestId,7);
assert.equal(output.sheet.rows.length,1000); assert.equal(output.sheet.rows[3][1],'kept');
assert.deepEqual(calls.slice(3).map(call => [Array.isArray(call.sheets)?Array.from(call.sheets):call.sheets,call.sheetRows]), [[[1],1],[[1],1000]]);
context.onmessage({data:{type:'load',index:2,requestId:8}});
assert.equal(output.sheet.rows.length,0); assert.equal(output.sheet.totalRows,10001);
assert.equal(output.sheet.truncated,true);
context.onmessage({data:{type:'load',index:3,requestId:9}});
assert.equal(output.sheet.rows.length,0); assert.equal(output.sheet.totalRows,0);
assert.equal(output.sheet.totalColumns,0); assert.equal(output.sheet.truncated,false);
`)
}

func TestSpreadsheetWorkerBoundsDelimitedPreview(t *testing.T) {
	runSpreadsheetWorkerNodeTest(t, `
const assert = require('node:assert/strict'), fs = require('node:fs'), vm = require('node:vm');
const NativeTextDecoder = TextDecoder, NativeUint8Array = Uint8Array;
let decodedBytes = 0, maxDecodedChunk = 0, delimiterScanBytes = 0, decodingStarted = false;
class TrackingTextDecoder extends NativeTextDecoder {
 decode(input, options) {
  decodingStarted = true;
  if (input) { decodedBytes += input.byteLength; maxDecodedChunk = Math.max(maxDecodedChunk, input.byteLength); }
  return super.decode(input, options);
 }
}
function TrackingUint8Array(buffer) {
 const bytes = new NativeUint8Array(buffer);
 return new Proxy(bytes, {get(target, key) {
  if (!decodingStarted && typeof key === 'string' && /^\d+$/.test(key)) delimiterScanBytes = Math.max(delimiterScanBytes, Number(key)+1);
  const value = Reflect.get(target, key, target);
  return typeof value === 'function' ? value.bind(target) : value;
 }});
}
const context = {importScripts(){}, Uint8Array:TrackingUint8Array, TextDecoder:TrackingTextDecoder, TextEncoder, postMessage(value){context.output=value}, close(){}};
vm.createContext(context);
vm.runInContext(fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8'), context);
function parseBuffer(buffer, format) {
 context.onmessage({data:{buffer,format}});
 return context.output.sheet;
}
function parse(text, format) { return parseBuffer(new TextEncoder().encode(text).buffer, format); }
let sheet = parse('\uFEFFName,Value\r\n"Alpha, Inc",42\r\n,\r\nBeta,84','csv');
assert.equal(sheet.totalRows,4); assert.equal(sheet.totalColumns,2);
assert.equal(JSON.stringify(sheet.rows),JSON.stringify([['Name','Value'],['Alpha, Inc','42'],['',''],['Beta','84']]));
assert.equal(sheet.truncated,false);
sheet = parse('Name,Notes\r\nAlpha,"line 1\r\nline ""2"""\r\nBeta,84\r\n','csv');
assert.equal(sheet.totalRows,3); assert.equal(sheet.rows[1][1],'line 1\r\nline "2"');
sheet = parse('a\tb\n1\t2','tsv');
assert.equal(JSON.stringify(sheet.rows),JSON.stringify([['a','b'],['1','2']]));
assert.equal(sheet.totalRows,2); assert.equal(sheet.totalColumns,2);
const denseText = Array.from({length:1500},(_,row)=>Array.from({length:120},(_,column)=>row+':'+column).join(',')).join('\n');
const denseBuffer = new TextEncoder().encode(denseText).buffer;
decodedBytes = 0; maxDecodedChunk = 0;
sheet = parseBuffer(denseBuffer,'csv');
assert.equal(sheet.rows.length,1000); assert.equal(sheet.rows[0].length,100);
assert.equal(sheet.totalRows,1500); assert.equal(sheet.totalColumns,120);
assert.equal(sheet.rows[999][99],'999:99'); assert.equal(sheet.truncated,true);
assert.equal(decodedBytes,denseBuffer.byteLength,'decode exactly one full streaming pass');
assert.ok(maxDecodedChunk<=65536,'decoded chunks must remain bounded');
sheet = parse(Array.from({length:10002},(_,index)=>'r'+index+',v').join('\n'),'csv');
assert.equal(sheet.rows.length,10001); assert.equal(sheet.totalRows,10002);
assert.equal(sheet.truncated,true);
sheet = parse('','csv');
assert.equal(sheet.totalRows,1); assert.equal(sheet.totalColumns,1);
assert.equal(JSON.stringify(sheet.rows),JSON.stringify([['']]));
const giantBuffer = new TextEncoder().encode('left,right,'+'x'.repeat(2*1024*1024)).buffer;
delimiterScanBytes = 0; decodingStarted = false; decodedBytes = 0; maxDecodedChunk = 0;
sheet = parseBuffer(giantBuffer,'csv');
assert.equal(sheet.totalColumns,3); assert.equal(sheet.rows[0][0],'left'); assert.equal(sheet.rows[0][1],'right');
assert.equal(sheet.rows[0][2].length,2*1024*1024);
assert.equal(delimiterScanBytes,0,'delimiter inference must not scan the source bytes before decoding');
assert.equal(decodedBytes,giantBuffer.byteLength,'the full CSV should be decoded once');
assert.ok(maxDecodedChunk<=65536,'decoded chunks must remain bounded');
const semicolonBuffer = new TextEncoder().encode('x'.repeat(70*1024)+';tail').buffer;
delimiterScanBytes = 0; decodingStarted = false; decodedBytes = 0; maxDecodedChunk = 0;
sheet = parseBuffer(semicolonBuffer,'csv');
assert.equal(sheet.totalRows,1); assert.equal(sheet.totalColumns,2);
assert.equal(sheet.rows[0][0].length,70*1024); assert.equal(sheet.rows[0][1],'tail');
assert.equal(delimiterScanBytes,0,'long first records must use the same single streaming pass for delimiter inference');
assert.equal(decodedBytes,semicolonBuffer.byteLength,'delimiter inference must not trigger a second decode pass');
assert.ok(maxDecodedChunk<=65536,'long first records must still decode in bounded chunks');
const quotedSemicolonBuffer = new TextEncoder().encode('"'+'x'.repeat(70*1024)+',|inside";tail').buffer;
decodedBytes = 0; maxDecodedChunk = 0;
sheet = parseBuffer(quotedSemicolonBuffer,'csv');
assert.equal(sheet.totalColumns,2); assert.equal(sheet.rows[0][0],'x'.repeat(70*1024)+',|inside');
assert.equal(sheet.rows[0][1],'tail'); assert.equal(decodedBytes,quotedSemicolonBuffer.byteLength);
assert.ok(maxDecodedChunk<=65536);
`)
}

func TestSpreadsheetWorkerLoadsWorksheetsOnDemand(t *testing.T) {
	runSpreadsheetWorkerNodeTest(t, `
(async()=>{
const assert = require('node:assert/strict'), fs = require('node:fs'), vm = require('node:vm');
const XLSX = require('../../assets/file-vendor/xlsx.full.min.js');
const workbook = XLSX.utils.book_new();
for (let sheet = 0; sheet < 2; sheet++) {
 const rows = Array.from({length:3000}, (_, row) => Array.from({length:120}, (_, col) => sheet*1000000+row*120+col));
 XLSX.utils.book_append_sheet(workbook, XLSX.utils.aoa_to_sheet(rows), 'Sheet'+(sheet+1));
}
const bytes = XLSX.write(workbook,{type:'array',bookType:'xlsx'});
const zipBytes=new Uint8Array(bytes),zipView=new DataView(zipBytes.buffer,zipBytes.byteOffset,zipBytes.byteLength);
let directoryEnd=-1;for(let offset=zipBytes.length-22;offset>=Math.max(0,zipBytes.length-65557);offset--)if(zipView.getUint32(offset,true)===0x06054b50){directoryEnd=offset;break}
assert.notEqual(directoryEnd,-1);
let directoryOffset=zipView.getUint32(directoryEnd+16,true),sheetXMLSize=0;
for(let entry=0;entry<zipView.getUint16(directoryEnd+10,true);entry++){
 const nameLength=zipView.getUint16(directoryOffset+28,true),extraLength=zipView.getUint16(directoryOffset+30,true),commentLength=zipView.getUint16(directoryOffset+32,true);
 const name=new TextDecoder().decode(zipBytes.subarray(directoryOffset+46,directoryOffset+46+nameLength));
 if(name==='xl/worksheets/sheet1.xml')sheetXMLSize=zipView.getUint32(directoryOffset+24,true);
 directoryOffset+=46+nameLength+extraLength+commentLength;
}
const calls = [], originalRead = XLSX.read, originalSplit = String.prototype.split;
const NativeDecompressionStream=DecompressionStream;
let worksheetSplitCalls = 0, inflatedBytes=0;
class CountingDecompressionStream {
 constructor(format){const native=new NativeDecompressionStream(format);this.writable=native.writable;this.readable=native.readable.pipeThrough(new TransformStream({transform(chunk,controller){inflatedBytes+=chunk.byteLength;controller.enqueue(chunk)}}))}
}
String.prototype.split = function(separator, ...args) {
 if (this.includes('<row') && this.includes('<c ')) worksheetSplitCalls++;
 return originalSplit.call(this,separator,...args);
};
XLSX.read = (input, options) => {
 calls.push({...options});
 return originalRead(input,options);
};
let output;
const context = {XLSX,TextDecoder,ReadableStream,DecompressionStream:CountingDecompressionStream,TransformStream,importScripts(){},postMessage(value){output=value},close(){}};
vm.createContext(context);
vm.runInContext(fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8'), context);
context.onmessage({data:{buffer:bytes,format:'xlsx'}});
await new Promise((resolve,reject)=>{const deadline=Date.now()+5000;(function poll(){if(output?.type==='ready')return resolve();if(output?.type==='error')return reject(Error(output.error));if(Date.now()>deadline)return reject(Error('XLSX preview did not finish'));setTimeout(poll,5)})()});
assert.equal(output.type,'ready');
assert.deepEqual(Array.from(output.names),['Sheet1','Sheet2']);
assert.equal(output.sheet.rows.length,1000);
assert.equal(output.sheet.totalRows,3000); assert.equal(output.sheet.totalColumns,120);
assert.equal(output.sheet.truncated,true);
assert.equal(output.sheet.rows[1099],undefined);
assert.equal(output.sheet.rows[999][99],'119979');
assert.equal(calls.length,1); assert.equal(Array.from(calls[0].sheets).length,0,'workbook styles and sheet paths must load without parsing a worksheet');
assert.equal(worksheetSplitCalls,0,'worksheet parsing must not split whole rows or cell fragments');
assert.ok(sheetXMLSize>0);
assert.ok(inflatedBytes<sheetXMLSize/2,'selected worksheet decompression must stop at the preview row bound');

context.onmessage({data:{type:'load',index:1,requestId:1}});
await new Promise((resolve,reject)=>{const deadline=Date.now()+5000;(function poll(){if(output?.type==='sheet'&&output.requestId===1)return resolve();if(output?.type==='error')return reject(Error(output.error));if(Date.now()>deadline)return reject(Error('XLSX worksheet selection did not finish'));setTimeout(poll,5)})()});
assert.equal(output.type,'sheet'); assert.equal(output.requestId,1);
assert.equal(output.sheet.rows.length,1000);
assert.equal(output.sheet.rows[999][99],'1119979');
assert.equal(output.sheet.totalRows,3000); assert.equal(output.sheet.totalColumns,120);
assert.equal(calls.length,1,'on-demand worksheet loads must not invoke the workbook-wide worksheet parser');
assert.equal(worksheetSplitCalls,0,'worksheet parsing must not split whole rows or cell fragments');
String.prototype.split = originalSplit;
})().catch(error=>{console.error(error);process.exitCode=1});
`)
}

func TestSpreadsheetWorkerSkipsWideXLSXRowTails(t *testing.T) {
	runSpreadsheetWorkerNodeTest(t, `
(async()=>{
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm'),XLSX=require('../../assets/file-vendor/xlsx.full.min.js');
const workbook=XLSX.utils.book_new();
XLSX.utils.book_append_sheet(workbook,XLSX.utils.aoa_to_sheet([Array.from({length:16000},(_,column)=>column)]),'Wide');
const bytes=XLSX.write(workbook,{type:'array',bookType:'xlsx',compression:true});
const zipView=XLSX.CFB.read(new Uint8Array(bytes),{type:'array'}),sheetXML=XLSX.CFB.find(zipView,'sheet1.xml').content;
const originalDecode=XLSX.utils.decode_cell;let decodedCells=0,worksheetInflatedBytes=0;
XLSX.utils.decode_cell=reference=>{if(typeof reference==='string'&&/^[A-Z]+\d+$/i.test(reference))decodedCells++;return originalDecode(reference)};
let output;
const context={XLSX,TextDecoder,ReadableStream,DecompressionStream,TransformStream,importScripts(){},postMessage(value){output=value},close(){}};
vm.createContext(context);vm.runInContext(fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8'),context);
const originalEntryStream=context.zipEntryStream;
context.zipEntryStream=(buffer,path)=>{const stream=originalEntryStream(buffer,path);return path==='xl/worksheets/sheet1.xml'&&stream?stream.pipeThrough(new TransformStream({transform(chunk,controller){worksheetInflatedBytes+=chunk.byteLength;controller.enqueue(chunk)}})):stream};
context.onmessage({data:{buffer:bytes,format:'xlsx'}});
await new Promise((resolve,reject)=>{const deadline=Date.now()+5000;(function poll(){if(output?.type==='ready')return resolve();if(output?.type==='error')return reject(Error(output.error));if(Date.now()>deadline)return reject(Error('wide worksheet preview did not finish'));setTimeout(poll,5)})()});
assert.equal(output.sheet.totalRows,1);assert.equal(output.sheet.totalColumns,16000);assert.equal(output.sheet.truncated,true);
assert.equal(output.sheet.rows[0].length,100);assert.equal(output.sheet.rows[0][99],'99');
assert.ok(decodedCells<300,'declared wide row tails must be skipped without decoding every cell coordinate');
assert.ok(worksheetInflatedBytes<sheetXML.length/2,'declared wide row tails must stop inflating before the full worksheet row is decoded');
})().catch(error=>{console.error(error);process.exitCode=1});
`)
}

func TestSpreadsheetWorkerPreservesLaterRowsInShortWideXLSX(t *testing.T) {
	runSpreadsheetWorkerNodeTest(t, `
(async()=>{
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm'),XLSX=require('../../assets/file-vendor/xlsx.full.min.js');
const workbook=XLSX.utils.book_new(),expected=[];
for(let sheet=0;sheet<2;sheet++){
 const rows=Array.from({length:2},(_,row)=>Array.from({length:120},(_,column)=>sheet*1000+row*120+column));
 expected.push(rows.map(row=>row.slice(0,100).map(String)));
 XLSX.utils.book_append_sheet(workbook,XLSX.utils.aoa_to_sheet(rows),'Wide'+sheet);
}
const bytes=XLSX.write(workbook,{type:'array',bookType:'xlsx',compression:true});
let output;
const context={XLSX,TextDecoder,ReadableStream,DecompressionStream,importScripts(){},postMessage(value){output=value},close(){}};
vm.createContext(context);vm.runInContext(fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8'),context);
const waitFor=type=>new Promise((resolve,reject)=>{const deadline=Date.now()+5000;(function poll(){if(output?.type===type)return resolve();if(output?.type==='error')return reject(Error(output.error));if(Date.now()>deadline)return reject(Error('wide multi-row worksheet did not finish'));setTimeout(poll,5)})()});
context.onmessage({data:{buffer:bytes,format:'xlsx'}});
await waitFor('ready');
for(let sheet=0;sheet<2;sheet++){
 if(sheet){context.onmessage({data:{type:'load',index:sheet,requestId:1}});await waitFor('sheet');assert.equal(output.requestId,1)}
 assert.equal(output.sheet.totalRows,2);assert.equal(output.sheet.totalColumns,120);assert.equal(output.sheet.truncated,true);
 assert.deepEqual(Array.from(output.sheet.rows,row=>Array.from(row)),expected[sheet],'every in-preview cell in both rows must survive skipping the first row tail');
}
})().catch(error=>{console.error(error);process.exitCode=1});
`)
}

func TestSpreadsheetWorkerPreservesValuesAcrossXMLChunks(t *testing.T) {
	runSpreadsheetWorkerNodeTest(t, `
(async()=>{
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm'),XLSX=require('../../assets/file-vendor/xlsx.full.min.js');
const rows=Array.from({length:5001},(_,r)=>Array.from({length:20},(_,c)=>r*20+c));
const wb=XLSX.utils.book_new();XLSX.utils.book_append_sheet(wb,XLSX.utils.aoa_to_sheet(rows),'Dense');
for(const compression of [false,true]){
 const buffer=XLSX.write(wb,{type:'array',bookType:'xlsx',compression});
 let ready,fail;
 const result=new Promise((resolve,reject)=>{ready=resolve;fail=reject});
 const context={XLSX,TextDecoder,ReadableStream,DecompressionStream,importScripts(){},close(){},postMessage(message){if(message.type==='ready')ready(message.sheet);else if(message.error)fail(Error(message.error))}};
 vm.createContext(context);vm.runInContext(fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8'),context);
 context.onmessage({data:{buffer,format:'xlsx'}});
 const sheet=await result;
 assert.equal(sheet.totalRows,5001);assert.equal(sheet.totalColumns,20);assert.equal(sheet.truncated,true);
 assert.deepEqual(JSON.parse(JSON.stringify(sheet.rows)),rows.slice(0,5000).map(row=>row.map(String)),'every value must survive XML chunk boundaries (compression='+compression+')');
}
})().catch(error=>{console.error(error);process.exitCode=1});
`)
}

func TestSpreadsheetWorkerPreservesXLSXTextAndDates(t *testing.T) {
	runSpreadsheetWorkerNodeTest(t, `
(async()=>{
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm'),XLSX=require('../../assets/file-vendor/xlsx.full.min.js');
const indexed=['  padded  ','_x0041_','_x005F_x0041_','_xD83D__xDE00_','&amp;','line_x000D_\nnext'];
const context={XLSX,importScripts(){},postMessage(){},close(){},fetch:async()=>({ok:true,json:async()=>Object.fromEntries(indexed.map((text,i)=>[i,text]))})};
vm.createContext(context);vm.runInContext(fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8'),context);
vm.runInContext("sharedStringsURL='/strings'",context);
const resolved=await context.selectedSharedStrings(null,null,new Set(indexed.map((_,i)=>i)));
for(let i=0;i<indexed.length;i++){
 const xml=indexed[i].replace(/&/g,'&amp;').replace(/</g,'&lt;');
 assert.equal(resolved[i].t,XLSX.parse_sst_item('<t>'+xml+'</t>',{cellHTML:false}).t,'indexed strings must match normal parser: '+i);
}
for(const date1904 of [false,true]){
 const wb=XLSX.utils.book_new();XLSX.utils.book_append_sheet(wb,XLSX.utils.aoa_to_sheet([[1],[2],[3],[4]]),'Values');
 wb.Workbook={WBProps:{date1904}};
 const bytes=XLSX.write(wb,{type:'array',bookType:'xlsx'});
 const zip=XLSX.CFB.read(new Uint8Array(bytes),{type:'array'}),entry=XLSX.CFB.find(zip,'sheet1.xml');
 const cases=[{type:'str',value:'  padded  '},{type:'str',value:'  _x0041_ &amp; _x005F_x0041_  '},{type:'d',value:'2024-02-29T12:34:56Z'},{type:'d',value:'2024-02-29'}];
 let xml=Buffer.from(entry.content).toString();
 xml=xml.replace(/<sheetData>[\s\S]*?<\/sheetData>/,'<sheetData>'+cases.map((c,i)=>'<row r="'+(i+1)+'"><c r="A'+(i+1)+'" t="'+c.type+'"><v>'+c.value+'</v></c></row>').join('')+'</sheetData>');
 entry.content=new TextEncoder().encode(xml);
 const normal=XLSX.read(new Uint8Array(XLSX.CFB.write(zip,{type:'array',fileType:'zip'})),{type:'array'}).Sheets.Values;
 context.metadata={Workbook:{WBProps:{date1904}},Styles:{CellXf:[{numFmtId:164}],NumberFmt:{164:'yyyy-mm-dd hh:mm:ss'}}};
 vm.runInContext('workbookMetadata=metadata',context);
 cases.forEach((c,i)=>{
  const expected=normal['A'+(i+1)];delete expected.w;expected.z='yyyy-mm-dd hh:mm:ss';
  const actual=context.xlsxCellFromXML({attributes:{t:c.type}},'<v>'+c.value+'</v>');
  assert.equal(actual.v,expected.v,'raw value: '+c.type+' / '+date1904);
  assert.equal(actual.w,XLSX.utils.format_cell(expected,undefined,{date1904}),'formatted value: '+c.type+' / '+date1904);
 });
}
})().catch(error=>{console.error(error);process.exitCode=1});
`)
}

func TestSpreadsheetWorkerSelectsSharedStringsForPreviewSheet(t *testing.T) {
	runSpreadsheetWorkerNodeTest(t, `
(async()=>{
const assert = require('node:assert/strict'), fs = require('node:fs'), vm = require('node:vm'), XLSX = require('../../assets/file-vendor/xlsx.full.min.js');
const workbook = XLSX.utils.book_new();
XLSX.utils.book_append_sheet(workbook, XLSX.utils.aoa_to_sheet([['VISIBLE_SHARED_VALUE']]), 'First');
XLSX.utils.book_append_sheet(workbook, XLSX.utils.aoa_to_sheet(Array.from({length:6000}, (_, index) => ['UNUSED_SHARED_VALUE_'+index])), 'Second');
const bytes = XLSX.write(workbook,{type:'array',bookType:'xlsx',bookSST:true});
const zipBytes = new Uint8Array(bytes), zipView = new DataView(zipBytes.buffer,zipBytes.byteOffset,zipBytes.byteLength);
let directoryEnd = -1;
for(let offset=zipBytes.length-22;offset>=Math.max(0,zipBytes.length-65557);offset--) if(zipView.getUint32(offset,true)===0x06054b50){directoryEnd=offset;break}
assert.notEqual(directoryEnd,-1);
let directoryOffset=zipView.getUint32(directoryEnd+16,true), targetEntry=null, sharedStringsSize=0;
for(let entry=0;entry<zipView.getUint16(directoryEnd+10,true);entry++){
 const nameLength=zipView.getUint16(directoryOffset+28,true), extraLength=zipView.getUint16(directoryOffset+30,true), commentLength=zipView.getUint16(directoryOffset+32,true);
 const name=new TextDecoder().decode(zipBytes.subarray(directoryOffset+46,directoryOffset+46+nameLength));
 if(name==='xl/worksheets/sheet2.xml') targetEntry={local:zipView.getUint32(directoryOffset+42,true),size:zipView.getUint32(directoryOffset+20,true)};
 if(name==='xl/sharedStrings.xml') sharedStringsSize=zipView.getUint32(directoryOffset+24,true);
 directoryOffset+=46+nameLength+extraLength+commentLength;
}
assert.ok(targetEntry&&targetEntry.size>0);
const compressedOffset=targetEntry.local+30+zipView.getUint16(targetEntry.local+26,true)+zipView.getUint16(targetEntry.local+28,true);
zipBytes.fill(0xff,compressedOffset,compressedOffset+targetEntry.size);
const originalParse = XLSX.parse_sst_item;
let parsed = [], inflatedBytes = 0;
XLSX.parse_sst_item = (xml, options) => { parsed.push(String(xml)); return originalParse(xml,options); };
const NativeDecompressionStream = DecompressionStream;
class CountingDecompressionStream {
 constructor(format) {
  const native = new NativeDecompressionStream(format);
  this.writable = native.writable;
  this.readable = native.readable.pipeThrough(new TransformStream({transform(chunk,controller){inflatedBytes+=chunk.byteLength;controller.enqueue(chunk)}}));
 }
}
let output;const messages=[];
const context = {XLSX,TextDecoder,ReadableStream,DecompressionStream:CountingDecompressionStream,importScripts(){},postMessage(value){messages.push(value);output=value},close(){}};
vm.createContext(context);
vm.runInContext(fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8'),context);
context.onmessage({data:{buffer:zipBytes.buffer,format:'xlsx'}});
await new Promise((resolve,reject)=>{const deadline=Date.now()+5000;(function poll(){if(messages.some(message=>message.type==='ready'))return resolve();if(output?.type==='error')return reject(Error(output.error));if(Date.now()>deadline)return reject(Error('worker did not publish selected sheet'));setTimeout(poll,5)})()});
await new Promise((resolve,reject)=>{const deadline=Date.now()+5000;(function poll(){if(messages.some(message=>message.type==='update'&&message.index===0))return resolve();if(output?.type==='error')return reject(Error(output.error));if(Date.now()>deadline)return reject(Error('worker did not resolve selected sheet text'));setTimeout(poll,5)})()});
assert.deepEqual(Array.from(output.names||messages.find(message=>message.type==='ready').names),['First','Second']);
assert.equal(output.sheet.rows[0][0],'VISIBLE_SHARED_VALUE');
assert.deepEqual(parsed.map(value=>value.replace(/<[^>]*>/g,'').trim()),['VISIBLE_SHARED_VALUE']);
assert.ok(sharedStringsSize>100000,'fixture must contain a large workbook-wide shared string table');
assert.ok(inflatedBytes<sharedStringsSize/2,'selected worksheet string resolution must stop decompressing before unused later-sheet strings');
assert.ok(output.sheet.rows[0][0].includes('VISIBLE_SHARED_VALUE'),'selected worksheet must remain readable when an unselected worksheet ZIP entry is corrupt');
})().catch(error=>{console.error(error);process.exitCode=1});
`)
}

func TestSpreadsheetWorkerPublishesPreviewBeforeHighIndexSharedStringResolution(t *testing.T) {
	runSpreadsheetWorkerNodeTest(t, `
(async()=>{
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm'),XLSX=require('../../assets/file-vendor/xlsx.full.min.js');
const workbook=XLSX.utils.book_new();
XLSX.utils.book_append_sheet(workbook,XLSX.utils.aoa_to_sheet([['VISIBLE_SHARED_VALUE','HIGH_INDEX_PLACEHOLDER']]),'First');
XLSX.utils.book_append_sheet(workbook,XLSX.utils.aoa_to_sheet(Array.from({length:6000},(_,index)=>['UNUSED_SHARED_VALUE_'+index])),'Second');
const originalBytes=XLSX.write(workbook,{type:'array',bookType:'xlsx',bookSST:true});
const archive=XLSX.CFB.read(new Uint8Array(originalBytes),{type:'array'}),firstSheet=XLSX.CFB.find(archive,'sheet1.xml'),secondSheet=XLSX.CFB.find(archive,'sheet2.xml');
let firstXML=new TextDecoder().decode(firstSheet.content),secondXML=new TextDecoder().decode(secondSheet.content);
assert.match(firstXML,/<c r="A1"[^>]*><v>0<\/v><\/c>/);
assert.match(firstXML,/<c r="B1"[^>]*><v>1<\/v><\/c>/);
assert.match(secondXML,/<c r="A1"[^>]*><v>2<\/v><\/c>/);
firstSheet.content=new TextEncoder().encode(firstXML.replace(/(<c r="B1"[^>]*><v>)1(<\/v><\/c>)/,'$16001$2'));
secondSheet.content=new TextEncoder().encode(secondXML.replace(/<sheetData>[\s\S]*?<\/sheetData>/,'<sheetData><row r="1"><c r="A1" t="s"><v>6001</v></c></row></sheetData>'));
const bytes=XLSX.CFB.write(archive,{type:'array',fileType:'zip',compression:true});
const zipBytes=new Uint8Array(bytes),zipView=new DataView(zipBytes.buffer,zipBytes.byteOffset,zipBytes.byteLength);
let directoryEnd=-1;for(let offset=zipBytes.length-22;offset>=Math.max(0,zipBytes.length-65557);offset--)if(zipView.getUint32(offset,true)===0x06054b50){directoryEnd=offset;break}
assert.notEqual(directoryEnd,-1);
let directoryOffset=zipView.getUint32(directoryEnd+16,true),sharedStringsSize=0,sharedStringsMethod=-1;
for(let entry=0;entry<zipView.getUint16(directoryEnd+10,true);entry++){
 const nameLength=zipView.getUint16(directoryOffset+28,true),extraLength=zipView.getUint16(directoryOffset+30,true),commentLength=zipView.getUint16(directoryOffset+32,true);
 const name=new TextDecoder().decode(zipBytes.subarray(directoryOffset+46,directoryOffset+46+nameLength));
 if(name==='xl/sharedStrings.xml'){sharedStringsSize=zipView.getUint32(directoryOffset+24,true);sharedStringsMethod=zipView.getUint16(directoryOffset+10,true)}
 directoryOffset+=46+nameLength+extraLength+commentLength;
}
assert.ok(sharedStringsSize>100000);
assert.equal(sharedStringsMethod,8,'the fixture must use one sequential DEFLATE stream for the shared-string table');
const originalParse=XLSX.parse_sst_item;let parsed=[];
XLSX.parse_sst_item=(xml,options)=>{parsed.push(String(xml));return originalParse(xml,options)};
let output;const messages=[];let sharedStringsInflatedBytes=0,inflatedAtReady=-1,inflatedAtFirstPartial=-1;
const context={XLSX,TextDecoder,ReadableStream,DecompressionStream,importScripts(){},postMessage(value){messages.push(value);output=value;if(value.type==='ready')inflatedAtReady=sharedStringsInflatedBytes;if(value.type==='update'&&inflatedAtFirstPartial<0)inflatedAtFirstPartial=sharedStringsInflatedBytes},close(){}};
vm.createContext(context);vm.runInContext(fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8'),context);
const streamedPaths=[],originalZipEntryStream=context.zipEntryStream;
context.zipEntryStream=(buffer,path)=>{
 streamedPaths.push(path);
 const stream=originalZipEntryStream(buffer,path);
 if(path==='xl/sharedStrings.xml'&&stream)return stream.pipeThrough(new TransformStream({transform(chunk,controller){sharedStringsInflatedBytes+=chunk.byteLength;controller.enqueue(chunk)}}));
 return stream;
};
context.onmessage({data:{buffer:zipBytes.buffer,format:'xlsx'}});
await new Promise((resolve,reject)=>{const deadline=Date.now()+5000;(function poll(){if(messages.some(message=>message.type==='ready'))return resolve();if(output?.type==='error')return reject(Error(output.error));if(Date.now()>deadline)return reject(Error('worker did not publish the bounded preview'));setTimeout(poll,5)})()});
const initial=messages.find(message=>message.type==='ready');
assert.equal(initial.sheet.rows[0][0],'','unresolved shared strings must not delay the bounded first preview');
assert.equal(initial.sheet.rows[0][1],'','unresolved high-index strings must remain placeholders initially');
assert.equal(initial.sheet.valuesPending,true,'the UI must identify placeholder values until the SST lookup finishes');
assert.ok(inflatedAtReady<sharedStringsSize/2,'first-sheet readiness must precede the high-index shared-string prefix scan');
await new Promise((resolve,reject)=>{const deadline=Date.now()+5000;(function poll(){if(messages.some(message=>message.type==='update'&&message.index===0&&message.sheet.rows[0][0]==='VISIBLE_SHARED_VALUE'))return resolve();if(output?.type==='error')return reject(Error(output.error));if(Date.now()>deadline)return reject(Error('worker did not progressively resolve the low-index shared string'));setTimeout(poll,5)})()});
const partial=messages.find(message=>message.type==='update'&&message.index===0&&message.sheet.rows[0][0]==='VISIBLE_SHARED_VALUE');
assert.equal(partial.sheet.rows[0][1],'','the high-index value should remain pending while earlier shared strings are available');
assert.equal(partial.sheet.valuesPending,true,'partially resolved previews must remain marked pending');
assert.ok(inflatedAtFirstPartial<sharedStringsSize/2,'the low-index cold value should resolve before the high-index SST prefix is inflated');
await new Promise((resolve,reject)=>{const deadline=Date.now()+5000;(function poll(){if(messages.some(message=>message.type==='update'&&message.index===0&&message.updatesPending===false))return resolve();if(output?.type==='error')return reject(Error(output.error));if(Date.now()>deadline)return reject(Error('worker did not resolve high-index shared string'));setTimeout(poll,5)})()});
assert.equal(output.sheet.rows[0][0],'VISIBLE_SHARED_VALUE');
assert.equal(output.sheet.rows[0][1],'UNUSED_SHARED_VALUE_5999');
assert.ok(sharedStringsInflatedBytes>=sharedStringsSize*0.95,'the final string should resolve after streaming its sequential SST prefix');
assert.equal(parsed.length,2,'only the requested low- and high-index strings should be materialized');
assert.match(parsed[1],/UNUSED_SHARED_VALUE_5999/);
assert.equal(vm.runInContext('sharedStringsCache.has(6001)',context),true,'resolved high-index strings should enter the bounded cache');
context.onmessage({data:{type:'load',index:1,requestId:22}});
await new Promise((resolve,reject)=>{const deadline=Date.now()+5000;(function poll(){if(messages.some(message=>message.type==='sheet'&&message.requestId===22))return resolve();if(output?.type==='error')return reject(Error(output.error));if(Date.now()>deadline)return reject(Error('on-demand worksheet did not publish its preview'));setTimeout(poll,5)})()});
await new Promise((resolve,reject)=>{const deadline=Date.now()+5000;(function poll(){if(messages.some(message=>message.type==='update'&&message.index===1))return resolve();if(output?.type==='error')return reject(Error(output.error));if(Date.now()>deadline)return reject(Error('on-demand worksheet did not reuse the cached high-index string'));setTimeout(poll,5)})()});
assert.equal(output.sheet.rows[0][0],'UNUSED_SHARED_VALUE_5999');
assert.equal(streamedPaths.filter(path=>path==='xl/sharedStrings.xml').length,1,'a high-index SST prefix should be decompressed at most once per worker');
vm.runInContext("for(let index=100000;index<120000;index++)cacheSharedString(index,{t:'x'.repeat(500)})",context);
assert.ok(vm.runInContext('sharedStringsCacheBytes<=MAX_SHARED_STRING_CACHE_BYTES',context),'shared-string cache payloads must have a fixed memory ceiling');
assert.ok(vm.runInContext('sharedStringsCache.size<=MAX_SHARED_STRING_CACHE_ENTRIES',context),'shared-string cache entry count must have a fixed ceiling');
let indexedOutput;const indexedMessages=[];let indexedInflated=0,indexedRequested=[];
let indexPolls=0;
const indexedContext={XLSX,TextDecoder,ReadableStream,DecompressionStream,TransformStream,setTimeout,importScripts(){},fetch:async(_url,request)=>{indexedRequested=JSON.parse(request.body);indexPolls++;return indexPolls===1?{ok:false,status:202}:{ok:true,status:200,json:async()=>({'0':'VISIBLE_SHARED_VALUE','6001':'UNUSED_SHARED_VALUE_5999'})}},postMessage(value){indexedMessages.push(value);indexedOutput=value},close(){}};
vm.createContext(indexedContext);vm.runInContext(fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8'),indexedContext);
const indexedStreamedPaths=[],indexedOriginalStream=indexedContext.zipEntryStream;
indexedContext.zipEntryStream=(buffer,path)=>{indexedStreamedPaths.push(path);const stream=indexedOriginalStream(buffer,path);return path==='xl/sharedStrings.xml'&&stream?stream.pipeThrough(new TransformStream({transform(chunk,controller){indexedInflated+=chunk.byteLength;controller.enqueue(chunk)}})):stream};
indexedContext.onmessage({data:{buffer:zipBytes.buffer,format:'xlsx',sharedStringsURL:'/file?spreadsheet-strings=1'}});
await new Promise((resolve,reject)=>{const deadline=Date.now()+5000;(function poll(){if(indexedMessages.some(message=>message.type==='ready'))return resolve();if(indexedOutput?.type==='error')return reject(Error(indexedOutput.error));if(Date.now()>deadline)return reject(Error('indexed worker did not publish preview'));setTimeout(poll,5)})()});
await new Promise((resolve,reject)=>{const deadline=Date.now()+5000;(function poll(){if(indexedMessages.some(message=>message.type==='update'&&message.updatesPending===false))return resolve();if(indexedOutput?.type==='error')return reject(Error(indexedOutput.error));if(Date.now()>deadline)return reject(Error('preindexed high-index string did not resolve'));setTimeout(poll,5)})()});
assert.ok(indexedRequested.includes(6001),'cold preview should request the high shared-string index from its precomputed sidecar');
assert.equal(indexPolls,2,'a missing sidecar should be polled asynchronously until preprocessing completes');
assert.equal(indexedOutput.sheet.rows[0][1],'UNUSED_SHARED_VALUE_5999');
assert.equal(indexedInflated,0,'preprocessed cold high-index values must resolve without inflating the original SST prefix');
assert.equal(indexedStreamedPaths.includes('xl/sharedStrings.xml'),false,'the indexed worker must not open the original SST entry');
let failedOutput;const failedMessages=[];
const failedContext={XLSX,TextDecoder,ReadableStream,DecompressionStream,TransformStream,importScripts(){},fetch:async()=>({ok:false,status:503}),postMessage(value){failedMessages.push(value);failedOutput=value},close(){}};
vm.createContext(failedContext);vm.runInContext(fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8'),failedContext);
const failedPaths=[],failedOriginalStream=failedContext.zipEntryStream;
failedContext.zipEntryStream=(buffer,path)=>{failedPaths.push(path);return failedOriginalStream(buffer,path)};
failedContext.onmessage({data:{buffer:zipBytes.buffer,format:'xlsx',sharedStringsURL:'/file?spreadsheet-strings=1'}});
await new Promise((resolve,reject)=>{const deadline=Date.now()+5000;(function poll(){if(failedMessages.some(message=>message.type==='update-error'))return resolve();if(failedOutput?.type==='error')return reject(Error(failedOutput.error));if(Date.now()>deadline)return reject(Error('index failure was not surfaced'));setTimeout(poll,5)})()});
assert.equal(failedPaths.includes('xl/sharedStrings.xml'),false,'an unavailable sidecar must not trigger browser-side SST prefix scanning');
})().catch(error=>{console.error(error);process.exitCode=1});
`)
}

func TestSpreadsheetWorkerBoundsOpenDocumentPreview(t *testing.T) {
	runSpreadsheetWorkerNodeTest(t, `
const assert = require('node:assert/strict'), fs = require('node:fs'), vm = require('node:vm');
const XLSX = require('../../assets/file-vendor/xlsx.full.min.js');
const workbook = XLSX.utils.book_new();
XLSX.utils.book_append_sheet(workbook, XLSX.utils.aoa_to_sheet([['repeated']]), 'Repeated');
XLSX.utils.book_append_sheet(workbook, XLSX.utils.aoa_to_sheet([['loaded on demand']]), 'Second');
const archived = XLSX.write(workbook,{type:'array',bookType:'ods'});
const archivedCatalog = XLSX.read(archived,{type:'array',bookSheets:true});
assert.deepEqual(archivedCatalog.SheetNames,['Repeated','Second']);
const archivedFirst = XLSX.read(archived,{type:'array',sheets:0,sheetRows:1,sheetCols:1});
assert.equal(archivedFirst.Sheets.Repeated.A1.v,'repeated');
assert.equal(archivedFirst.Sheets.Second.A1,undefined,'ODS probes must skip unselected worksheets');
const archivedSecond = XLSX.read(archived,{type:'array',sheets:[1],sheetRows:1,sheetCols:1});
assert.equal(archivedSecond.Sheets.Second.A1.v,'loaded on demand');
assert.equal(archivedSecond.Sheets.Repeated.A1,undefined);
let flat = XLSX.write(workbook,{type:'string',bookType:'fods'});
const secondTableStart = flat.indexOf('<table:table table:name="Second"');
const secondTableOpenEnd = flat.indexOf('>',secondTableStart)+1;
const skippedTagFlat = flat.slice(0,secondTableOpenEnd)+'<evil:unrecognized/>'+flat.slice(secondTableOpenEnd);
assert.throws(()=>XLSX.read(skippedTagFlat,{type:'string',WTF:true}),'the fixture must fail if the unselected worksheet body is traversed');
const selectedWithoutTraversal = XLSX.read(skippedTagFlat,{type:'string',sheets:0,WTF:true});
assert.equal(selectedWithoutTraversal.Sheets.Repeated.A1.v,'repeated');
assert.deepEqual(selectedWithoutTraversal.SheetNames,['Repeated','Second']);
const catalogWithoutTraversal = XLSX.read(skippedTagFlat,{type:'string',bookSheets:true,WTF:true});
assert.deepEqual(catalogWithoutTraversal.SheetNames,['Repeated','Second']);
flat = flat.replace('<table:table-row>', '<table:table-row table:number-rows-repeated="10002">');
flat = flat.replace('<table:table-cell office:value-type="string">', '<table:table-cell table:number-columns-repeated="16384" office:value-type="string">');
const bytes = new TextEncoder().encode(flat).buffer;
const calls = [], parsedSheets = [], originalRead = XLSX.read;
XLSX.read = (input, options) => {
 const result = originalRead(input,options);
 calls.push({...options});
 if (options.sheets !== undefined) parsedSheets.push(result.Sheets);
 return result;
};
let output;
const context = {XLSX, importScripts(){}, postMessage(value){output=value}, close(){}};
vm.createContext(context);
vm.runInContext(fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8'), context);
context.onmessage({data:{buffer:bytes,format:'ods'}});
assert.equal(output.type,'ready');
assert.deepEqual(Array.from(output.names),['Repeated','Second']);
assert.equal(output.sheet.totalRows,10002); assert.equal(output.sheet.totalColumns,16384);
assert.equal(output.sheet.rows.length,1000); assert.equal(output.sheet.rows[0].length,100);
assert.equal(output.sheet.rows[0][0],'repeated'); assert.equal(output.sheet.rows[999][99],'repeated');
assert.equal(output.sheet.truncated,true);
const bounded = parsedSheets[1].Repeated;
assert.equal(bounded['!ref'],'A1:CV1000'); assert.equal(bounded['!fullref'],'A1:XFD10002');
assert.equal(bounded.CW1,undefined); assert.equal(bounded.A1001,undefined);
assert.ok(Object.keys(bounded).filter(key=>!key.startsWith('!')).length<=100000,'repeated ODS cells must be materialized only within preview bounds');
assert.ok(!parsedSheets[0].Second.A1,'ODS sheet metadata reads must not materialize other worksheet cells');
context.onmessage({data:{type:'load',index:1,requestId:5}});
assert.equal(output.type,'sheet'); assert.equal(output.requestId,5);
assert.equal(output.sheet.rows[0][0],'loaded on demand');
assert.equal(output.sheet.totalRows,1); assert.equal(output.sheet.totalColumns,1);
assert.deepEqual(calls.slice(3).map(call=>[Array.isArray(call.sheets)?Array.from(call.sheets):call.sheets,call.sheetRows]), [[[1],1]]);
`)
}

func TestSpreadsheetWorkerBoundsSelectedOpenDocumentTableXML(t *testing.T) {
	runSpreadsheetWorkerNodeTest(t, `
(async()=>{
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm'),XLSX=require('../../assets/file-vendor/xlsx.full.min.js');
const workbook=XLSX.utils.book_new();
const rows=Array.from({length:2500},(_,row)=>Array.from({length:120},(_,column)=>row*120+column));
XLSX.utils.book_append_sheet(workbook,XLSX.utils.aoa_to_sheet(rows),'Dense');
const bytes=XLSX.write(workbook,{type:'array',bookType:'ods',compression:true});
const zip=new Uint8Array(bytes),view=new DataView(zip.buffer,zip.byteOffset,zip.byteLength);
let eocd=-1;for(let offset=zip.length-22;offset>=Math.max(0,zip.length-65557);offset--)if(view.getUint32(offset,true)===0x06054b50){eocd=offset;break}
assert.notEqual(eocd,-1);
let directory=view.getUint32(eocd+16,true),contentXMLSize=0;
for(let entry=0;entry<view.getUint16(eocd+10,true);entry++){
 const nameLength=view.getUint16(directory+28,true),extraLength=view.getUint16(directory+30,true),commentLength=view.getUint16(directory+32,true);
 const name=new TextDecoder().decode(zip.subarray(directory+46,directory+46+nameLength));
 if(name==='content.xml')contentXMLSize=view.getUint32(directory+24,true);
 directory+=46+nameLength+extraLength+commentLength;
}
assert.ok(contentXMLSize>0);
const originalRead=XLSX.read,parsedXML=[],messages=[];
XLSX.read=(input,options)=>{if(typeof input==='string'&&input.includes('<office:document'))parsedXML.push(input.length);return originalRead(input,options)};
let output;
const context={XLSX,TextDecoder,ReadableStream,DecompressionStream,setTimeout,importScripts(){},postMessage(value){messages.push(value);output=value},close(){}};
vm.createContext(context);vm.runInContext(fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8'),context);
context.onmessage({data:{buffer:bytes,format:'ods'}});
await new Promise((resolve,reject)=>{const deadline=Date.now()+15000;(function poll(){if(output?.type==='ready')return resolve();if(output?.type==='error')return reject(Error(output.error));if(Date.now()>deadline)return reject(Error('large ODS preview did not finish'));setTimeout(poll,5)})()});
await new Promise((resolve,reject)=>{const deadline=Date.now()+15000;(function poll(){if(messages.some(message=>message.type==='update'))return resolve();if(output?.type==='error')return reject(Error(output.error));if(Date.now()>deadline)return reject(Error('large ODS dimensions were not finalized'));setTimeout(poll,5)})()});
const final=messages.filter(message=>message.type==='update'&&message.index===0).at(-1);
assert.equal(final.sheet.totalRows,2500);assert.equal(final.sheet.totalColumns,120);
assert.equal(final.sheet.rows.length,1000);assert.equal(final.sheet.rows[0][0],'0');assert.equal(final.sheet.rows[999][99],'119979');
assert.equal(final.sheet.truncated,true);
assert.equal(parsedXML.length,2,'selected ODS preview and final dimensions should each parse bounded XML once');
assert.ok(parsedXML.every(size=>size<contentXMLSize/2),'selected ODS parsing input must be reduced to bounded preview rows and columns');
})().catch(error=>{console.error(error);process.exitCode=1});
`)
}

func TestSpreadsheetWorkerBoundsODSInflationOutput(t *testing.T) {
	runSpreadsheetWorkerNodeTest(t, `
(async()=>{
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm'),zlib=require('node:zlib');
const context={ReadableStream,importScripts(){},postMessage(){},close(){}};
vm.createContext(context);vm.runInContext(fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8'),context);
const input=Buffer.concat([Buffer.from('preview cell repeated with a long back-reference '.repeat(20000)),Buffer.from(Array.from({length:70000},(_,index)=>(index*71+Math.floor(index/139))%256))]);
const collect=async bytes=>{const chunks=[];for await(const chunk of context.boundedInflateStream(bytes)){assert.ok(chunk.byteLength<=4096,'ODS inflation output must be bounded before decoding, regardless of compression ratio');chunks.push(Buffer.from(chunk))}return Buffer.concat(chunks)};
const blockTypes=new Set();
for(const options of [{level:0},{strategy:zlib.constants.Z_FIXED},{level:9}]){
 const compressed=zlib.deflateRawSync(input,options);blockTypes.add((compressed[0]>>>1)&3);
 assert.deepEqual(await collect(compressed),input,'stored, fixed, and dynamic blocks must preserve exact bytes and long history references');
}
assert.deepEqual([...blockTypes].sort(),[0,1,2],'fixtures must exercise all three valid DEFLATE block types');
const compressor=zlib.createDeflateRaw(),parts=[];
compressor.on('data',part=>parts.push(part));
const ended=new Promise((resolve,reject)=>{compressor.on('end',resolve);compressor.on('error',reject)});
compressor.write(input.subarray(0,54321));
await new Promise((resolve,reject)=>compressor.flush(zlib.constants.Z_SYNC_FLUSH,error=>error?reject(error):resolve()));
compressor.end(input.subarray(54321));await ended;
assert.deepEqual(await collect(Buffer.concat(parts)),input,'history must survive block boundaries and sync-flush stored blocks');
await assert.rejects(()=>collect(Buffer.from([7])),/DEFLATE block/);
await assert.rejects(()=>collect(Buffer.from([1,0,0,0,0])),/stored block/);
const compressed=zlib.deflateRawSync(input,{level:9});
await assert.rejects(()=>collect(compressed.subarray(0,compressed.length-1)),/Truncated/);
const reader=context.boundedInflateStream(compressed).getReader();
assert.equal((await reader.read()).value.byteLength,4096);await reader.cancel();assert.equal((await reader.read()).done,true);
})().catch(error=>{console.error(error);process.exitCode=1});
`)
}

func TestSpreadsheetWorkerStreamsOpenDocumentBeforeUnselectedSheets(t *testing.T) {
	runSpreadsheetWorkerNodeTest(t, `
(async()=>{
const assert = require('node:assert/strict'), fs = require('node:fs'), vm = require('node:vm');
const XLSX = require('../../assets/file-vendor/xlsx.full.min.js');
const workbook = XLSX.utils.book_new();
XLSX.utils.book_append_sheet(workbook, XLSX.utils.aoa_to_sheet(Array.from({length:1200},(_,row)=>[row===0?'first preview ready':'previous row '+row])), 'First');
XLSX.utils.book_append_sheet(workbook, XLSX.utils.aoa_to_sheet([['second']]), 'Second');
const bytes = XLSX.write(workbook,{type:'array',bookType:'ods',compression:true});
const zip = new Uint8Array(bytes), view = new DataView(zip.buffer,zip.byteOffset,zip.byteLength);
let eocd=-1;for(let offset=zip.length-22;offset>=Math.max(0,zip.length-65557);offset--)if(view.getUint32(offset,true)===0x06054b50){eocd=offset;break}
assert.notEqual(eocd,-1);
let directory=view.getUint32(eocd+16,true), contentMethod=0;
for(let i=0;i<view.getUint16(eocd+10,true);i++){
 const nameLength=view.getUint16(directory+28,true), extraLength=view.getUint16(directory+30,true), commentLength=view.getUint16(directory+32,true);
 const name=new TextDecoder().decode(zip.subarray(directory+46,directory+46+nameLength));
 if(name==='content.xml')contentMethod=view.getUint16(directory+10,true);
 directory+=46+nameLength+extraLength+commentLength;
}
assert.equal(contentMethod,8,'fixture must exercise incremental ZIP decompression');
const flat=XLSX.write(workbook,{type:'string',bookType:'fods'}).replace('<office:document ','<office:document-content ').replace('</office:document>','</office:document-content>');
const firstClose=flat.indexOf('</table:table>')+'</table:table>'.length;
const first=flat.slice(0,firstClose);
const rest=flat.slice(firstClose).replace('<table:table-row>','<table:table-row><evil:unrecognized/>');
let releaseWriter, firstWritten=false, completed=false, output;
const NativeDecompressionStream=DecompressionStream, encoder=new TextEncoder();
class GatedDecompressionStream {
 constructor(){
  let controller;
  this.readable=new ReadableStream({start(value){controller=value}});
  this.writable=new WritableStream({
   write(){if(!firstWritten){firstWritten=true;controller.enqueue(encoder.encode(first));return new Promise(resolve=>{releaseWriter=resolve})}},
   close(){controller.enqueue(encoder.encode(rest));controller.close();completed=true}
  });
 }
}
const context={XLSX,TextDecoder,ReadableStream,DecompressionStream:GatedDecompressionStream,setTimeout,importScripts(){},postMessage(value){output=value},close(){}};
vm.createContext(context);
vm.runInContext(fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8'),context);
const originalInflater=context.boundedInflateStream;
context.boundedInflateStream=compressed=>new ReadableStream({start(controller){controller.enqueue(compressed);controller.close()}}).pipeThrough(new GatedDecompressionStream());
let unselectedCellTokenChunks=0;const originalConsume=context.consumeODSTable;
context.consumeODSTable=(active,text,...args)=>{if(!active.capture&&text.includes('<table:table-cell'))unselectedCellTokenChunks++;return originalConsume(active,text,...args)};
context.onmessage({data:{buffer:zip.buffer,format:'ods'}});
await new Promise((resolve,reject)=>{const deadline=Date.now()+5000;(function poll(){if(output?.type==='ready')return resolve();if(output?.type==='error')return reject(Error(output.error));if(Date.now()>deadline)return reject(Error('ODS preview waited for unselected worksheet data'));setTimeout(poll,5)})()});
assert.equal(output.sheet.rows[0][0],'first preview ready');
assert.deepEqual(Array.from(output.names),['First']);
assert.equal(completed,false,'worker must publish the first preview before consuming later worksheet XML');
releaseWriter();
await new Promise((resolve,reject)=>{const deadline=Date.now()+5000;(function poll(){if(output?.type==='catalog')return resolve();if(output?.type==='error')return reject(Error(output.error));if(Date.now()>deadline)return reject(Error('ODS sheet catalog was not completed'));setTimeout(poll,5)})()});
assert.equal(completed,true);
assert.deepEqual(Array.from(output.names),['First','Second']);
assert.equal(unselectedCellTokenChunks,0,'catalog discovery must skip unselected worksheet cell tokens');
context.DecompressionStream=NativeDecompressionStream;
context.boundedInflateStream=originalInflater;
context.onmessage({data:{type:'load',index:1,requestId:17}});
await new Promise((resolve,reject)=>{const deadline=Date.now()+5000;(function poll(){if(output?.type==='sheet'&&output.requestId===17)return resolve();if(output?.type==='error')return reject(Error(output.error));if(Date.now()>deadline)return reject(Error('ODS worksheet selection did not finish'));setTimeout(poll,5)})()});
assert.equal(output.sheet.rows[0][0],'second');
assert.deepEqual(Array.from(output.names),['First','Second']);
assert.equal(unselectedCellTokenChunks,0,'loading the second sheet must skip the previously measured first table body');
assert.equal(vm.runInContext('dimensions.get(0).totalRows',context),1200,'skipping a measured table must preserve its dimensions for workbook allocation');
})().catch(error=>{console.error(error);process.exitCode=1});
`)
}

func TestSpreadsheetWorkerShowsBoundedOpenDocumentPreviewBeforeTableEnd(t *testing.T) {
	runSpreadsheetWorkerNodeTest(t, `
(async()=>{
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm'),XLSX=require('../../assets/file-vendor/xlsx.full.min.js');
const rows=Array.from({length:11000},(_,row)=>'<table:table-row><table:table-cell office:value-type="string"><text:p>row'+row+'</text:p></table:table-cell></table:table-row>');
const root='<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0">';
const flat=root+'<office:body><office:spreadsheet><table:table table:name="Large">'+rows.join('')+'</table:table></office:spreadsheet></office:body></office:document-content>';
const workbook=XLSX.utils.book_new();XLSX.utils.book_append_sheet(workbook,XLSX.utils.aoa_to_sheet([['zip fixture']]),'Large');
const zipResult=XLSX.write(workbook,{type:'array',bookType:'ods',compression:true}),zip=zipResult instanceof ArrayBuffer?zipResult:zipResult.buffer;
let completed=false,output,tagsProcessed=0,tagsProcessedAtReady=-1;const messages=[];
const first=flat,rest='',encoder=new TextEncoder();let releaseWriter;
class GatedDecompressionStream{
 constructor(){let controller;this.readable=new ReadableStream({start(value){controller=value}});this.writable=new WritableStream({write(){if(!releaseWriter){controller.enqueue(encoder.encode(first));return new Promise(resolve=>{releaseWriter=resolve})}},close(){controller.enqueue(encoder.encode(rest));controller.close();completed=true}})}
}
let maximumDecodedBytes=0,maximumPendingBytes=0;
class BoundedTextDecoder extends TextDecoder{decode(bytes,options){maximumDecodedBytes=Math.max(maximumDecodedBytes,bytes?.byteLength||0);return super.decode(bytes,options)}}
const context={XLSX,TextDecoder:BoundedTextDecoder,ReadableStream,DecompressionStream:GatedDecompressionStream,setTimeout,recordPending(text){maximumPendingBytes=Math.max(maximumPendingBytes,Buffer.byteLength(text,'utf8'))},importScripts(){},postMessage(value){messages.push(value);output=value;if(value.type==='ready')tagsProcessedAtReady=tagsProcessed},close(){}};
const workerSource=fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8');
assert.ok(workerSource.includes('pending += text;'));
vm.createContext(context);vm.runInContext(workerSource.replace('pending += text;','pending += text; recordPending(pending);'),context);
context.boundedInflateStream=compressed=>new ReadableStream({start(controller){controller.enqueue(compressed);controller.close()}}).pipeThrough(new GatedDecompressionStream());
const originalTagEnd=context.xmlTagEnd;context.xmlTagEnd=(text,start)=>{tagsProcessed++;return originalTagEnd(text,start)};
context.onmessage({data:{buffer:zip,format:'ods'}});
await new Promise((resolve,reject)=>{const deadline=Date.now()+10000;(function poll(){if(messages.some(message=>message.type==='ready'))return resolve();if(output?.type==='error')return reject(Error(output.error));if(Date.now()>deadline)return reject(Error('ODS preview waited for the selected table to finish'));setTimeout(poll,5)})()});
const early=messages.find(message=>message.type==='ready');
assert.equal(completed,false,'bounded ODS preview must publish before the selected table XML is fully consumed');
assert.equal(early.sheet.rows.length,10001,JSON.stringify(early));assert.equal(early.sheet.rows[9999][0],'row9999');
assert.equal(early.sheet.dimensionsPending,true,'early observed dimensions must not be presented as final');
assert.equal(early.sheet.truncated,null,'truncation is unknown until the dimension scan finishes');
assert.ok(maximumDecodedBytes<=4096,'a whole-table output chunk must be decoded only in fixed-size byte windows');
assert.ok(maximumPendingBytes<=8192,'retained decoded input must not contain the whole-table chunk remainder');
releaseWriter();
await new Promise((resolve,reject)=>{const deadline=Date.now()+10000;(function poll(){if(messages.some(message=>message.type==='update'&&message.index===0))return resolve();if(output?.type==='error')return reject(Error(output.error));if(Date.now()>deadline)return reject(Error('ODS final dimensions were not reported'));setTimeout(poll,5)})()});
const final=messages.filter(message=>message.type==='update'&&message.index===0).at(-1);
await new Promise((resolve,reject)=>{const deadline=Date.now()+5000;(function poll(){if(completed)return resolve();if(Date.now()>deadline)return reject(Error('ODS decompressor did not finish after release'));setTimeout(poll,5)})()});
assert.equal(final.sheet.totalRows,11000);assert.equal(final.sheet.totalColumns,1);assert.equal(final.sheet.rows[10000][0],'row10000');assert.equal(final.sheet.truncated,true);assert.equal(completed,true);
assert.ok(tagsProcessedAtReady>0&&tagsProcessed>tagsProcessedAtReady,'ODS must publish at the preview row cap before traversing the rest of a single decompressor chunk');
assert.equal(final.sheet.dimensionsPending,false,'final dimensions must replace provisional metadata');
assert.ok(maximumDecodedBytes<=4096&&maximumPendingBytes<=8192,'the decoded input bound must hold through the entire table');
})().catch(error=>{console.error(error);process.exitCode=1});
`)
}

func runSpreadsheetWorkerNodeTest(t *testing.T, script string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("spreadsheet worker: %v\n%s", err, output)
	}
}
