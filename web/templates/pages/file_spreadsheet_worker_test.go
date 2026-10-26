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
assert.ok(delimiterScanBytes<=65536,'delimiter inference must stop at the fixed sample bound');
assert.equal(decodedBytes,giantBuffer.byteLength,'the full CSV should be decoded once');
assert.ok(maxDecodedChunk<=65536,'decoded chunks must remain bounded');
`)
}

func TestSpreadsheetWorkerLoadsWorksheetsOnDemand(t *testing.T) {
	runSpreadsheetWorkerNodeTest(t, `
const assert = require('node:assert/strict'), fs = require('node:fs'), vm = require('node:vm');
const XLSX = require('../../assets/file-vendor/xlsx.full.min.js');
const workbook = XLSX.utils.book_new();
for (let sheet = 0; sheet < 2; sheet++) {
 const rows = Array.from({length:1100}, (_, row) => Array.from({length:120}, (_, col) => String(sheet)+':'+row+':'+col));
 XLSX.utils.book_append_sheet(workbook, XLSX.utils.aoa_to_sheet(rows), 'Sheet'+(sheet+1));
}
const bytes = XLSX.write(workbook,{type:'array',bookType:'xlsx'});
const calls = [], parsedSheets = [], originalRead = XLSX.read, originalSplit = String.prototype.split;
let worksheetSplitCalls = 0;
String.prototype.split = function(separator, ...args) {
 if (this.includes('<row') && this.includes('<c ')) worksheetSplitCalls++;
 return originalSplit.call(this,separator,...args);
};
XLSX.read = (input, options) => {
 calls.push({...options});
 const result = originalRead(input,options);
 if (options.sheets !== undefined) parsedSheets.push(result.Sheets);
 return result;
};
let output;
const context = {XLSX, importScripts(){}, postMessage(value){output=value}, close(){}};
vm.createContext(context);
vm.runInContext(fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8'), context);
context.onmessage({data:{buffer:bytes,format:'xlsx'}});
assert.equal(output.type,'ready');
assert.deepEqual(Array.from(output.names),['Sheet1','Sheet2']);
assert.equal(output.sheet.rows.length,1000);
assert.equal(output.sheet.totalRows,1100); assert.equal(output.sheet.totalColumns,120);
assert.equal(output.sheet.truncated,true);
assert.equal(output.sheet.rows[1099],undefined);
assert.equal(output.sheet.rows[999][99],'0:999:99');
const firstParsed = parsedSheets[1].Sheet1;
assert.equal(firstParsed[XLSX.utils.encode_cell({r:0,c:100})],undefined,'cells beyond the 100-column parse bound must not be materialized');
assert.equal(firstParsed[XLSX.utils.encode_cell({r:0,c:119})],undefined,'far-right cells must not be materialized');
assert.equal(firstParsed['!fullref'],'A1:DP1100','full worksheet dimensions must survive bounded parsing');
assert.deepEqual(calls.map(call => [call.bookSheets||false,Array.isArray(call.sheets)?Array.from(call.sheets):call.sheets,call.sheetRows]), [[true,undefined,undefined],[false,0,1],[false,[0],1000]]);

context.onmessage({data:{type:'load',index:1,requestId:1}});
assert.equal(output.type,'sheet'); assert.equal(output.requestId,1);
assert.equal(output.sheet.rows.length,1000);
assert.equal(output.sheet.rows[999][99],'1:999:99');
assert.equal(output.sheet.totalRows,1100); assert.equal(output.sheet.totalColumns,120);
const secondParsed = parsedSheets[3].Sheet2;
assert.equal(secondParsed[XLSX.utils.encode_cell({r:0,c:100})],undefined,'on-demand worksheets must use the same parse column bound');
assert.deepEqual(calls.slice(3).map(call => [Array.isArray(call.sheets)?Array.from(call.sheets):call.sheets,call.sheetRows]), [[[1],1],[[1],1000]]);
assert.equal(worksheetSplitCalls,0,'worksheet parsing must not split whole rows or cell fragments');
String.prototype.split = originalSplit;
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
