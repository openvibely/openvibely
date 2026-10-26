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
const context = {importScripts(){}, TextDecoder, postMessage(value){context.output=value}, close(){}};
vm.createContext(context);
vm.runInContext(fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8'), context);
function parse(text, format) {
 const buffer = new TextEncoder().encode(text).buffer;
 context.onmessage({data:{buffer,format}});
 return context.output.sheet;
}
let sheet = parse('Name,Value\n"Alpha, Inc",42\n,\nBeta,84','csv');
assert.equal(sheet.totalRows,4); assert.equal(sheet.totalColumns,2);
assert.equal(JSON.stringify(sheet.rows),JSON.stringify([['Name','Value'],['Alpha, Inc','42'],['',''],['Beta','84']]));
assert.equal(sheet.truncated,false);
sheet = parse('a\tb\n1\t2','tsv');
assert.equal(JSON.stringify(sheet.rows),JSON.stringify([['a','b'],['1','2']]));
assert.equal(sheet.totalRows,2); assert.equal(sheet.totalColumns,2);
sheet = parse(Array.from({length:10002},(_,index)=>'r'+index+',v').join('\n'),'csv');
assert.equal(sheet.rows.length,10001); assert.equal(sheet.totalRows,10002);
assert.equal(sheet.truncated,true);
sheet = parse('','csv');
assert.equal(sheet.totalRows,1); assert.equal(sheet.totalColumns,1);
assert.equal(JSON.stringify(sheet.rows),JSON.stringify([['']]));
`)
}

func TestSpreadsheetWorkerLoadsWorksheetsOnDemand(t *testing.T) {
	runSpreadsheetWorkerNodeTest(t, `
const assert = require('node:assert/strict'), fs = require('node:fs'), vm = require('node:vm');
const XLSX = require('../../assets/file-vendor/xlsx.full.min.js');
const workbook = XLSX.utils.book_new();
for (let sheet = 0; sheet < 2; sheet++) {
 const rows = Array.from({length:1100}, (_, row) => Array.from({length:100}, (_, col) => String(sheet)+':'+row+':'+col));
 XLSX.utils.book_append_sheet(workbook, XLSX.utils.aoa_to_sheet(rows), 'Sheet'+(sheet+1));
}
const bytes = XLSX.write(workbook,{type:'array',bookType:'xlsx'});
const calls = [], originalRead = XLSX.read;
XLSX.read = (input, options) => {calls.push({...options});return originalRead(input,options)};
let output;
const context = {XLSX, importScripts(){}, postMessage(value){output=value}, close(){}};
vm.createContext(context);
vm.runInContext(fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8'), context);
context.onmessage({data:{buffer:bytes,format:'xlsx'}});
assert.equal(output.type,'ready');
assert.deepEqual(Array.from(output.names),['Sheet1','Sheet2']);
assert.equal(output.sheet.rows.length,1000);
assert.equal(output.sheet.totalRows,1100); assert.equal(output.sheet.totalColumns,100);
assert.equal(output.sheet.truncated,true);
assert.equal(output.sheet.rows[1099],undefined);
assert.equal(output.sheet.rows[999][99],'0:999:99');
assert.deepEqual(calls.map(call => [call.bookSheets||false,Array.isArray(call.sheets)?Array.from(call.sheets):call.sheets,call.sheetRows]), [[true,undefined,undefined],[false,0,1],[false,[0],1000]]);

context.onmessage({data:{type:'load',index:1,requestId:1}});
assert.equal(output.type,'sheet'); assert.equal(output.requestId,1);
assert.equal(output.sheet.rows.length,1000);
assert.equal(output.sheet.rows[999][99],'1:999:99');
assert.equal(output.sheet.totalRows,1100); assert.equal(output.sheet.totalColumns,100);
assert.deepEqual(calls.slice(3).map(call => [Array.isArray(call.sheets)?Array.from(call.sheets):call.sheets,call.sheetRows]), [[[1],1],[[1],1000]]);
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
