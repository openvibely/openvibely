package pages

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestSpreadsheetWorkerBoundsSparsePreview(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node required")
	}
	script := `
const assert = require('node:assert/strict'), fs = require('node:fs'), vm = require('node:vm');
const XLSX = require('../../assets/file-vendor/xlsx.full.min.js');
const workbook = XLSX.utils.book_new();
for (let i = 0; i < 3; i++) {
 XLSX.utils.book_append_sheet(workbook, {'!ref':'A1:XFD10001', B4:{t:'s',v:'kept'}, XFD10001:{t:'s',v:'far corner'}}, 'Wide'+i);
}
XLSX.utils.book_append_sheet(workbook, {}, 'Empty');
// Supply the parsed sparse workbook directly: fixture serialization also walks
// the entire declared range, which is unrelated to the preview allocation.
XLSX.read = () => workbook;
const input = new ArrayBuffer(0);
// Fail before allocating if a regression requests the original 163 million cells.
const original = XLSX.utils.sheet_to_json;
let allocated = 0;
XLSX.utils.sheet_to_json = function(sheet, options) {
 const range = options.range, rows = range.e.r-range.s.r+1, columns = range.e.c-range.s.c+1;
 assert.equal(range.s.r,0); assert.equal(range.s.c,0);
 assert.ok(columns <= 100, 'column cap must apply before expansion');
 assert.ok(rows * columns <= 100000, 'per-sheet cell cap must apply before expansion');
 allocated += rows * columns;
 assert.ok(allocated <= 200000, 'workbook allocation must be bounded');
 return original(sheet, options);
};
let output;
const context = {XLSX, TextDecoder, importScripts(){}, postMessage(value){output=value}, close(){}};
vm.createContext(context);
vm.runInContext(fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8'), context);
context.onmessage({data:input});
assert.equal(output.error, undefined);
assert.equal(output.sheets.length,4);
for (const sheet of output.sheets.slice(0,3)) {
 assert.equal(sheet.totalRows,10001); assert.equal(sheet.totalColumns,16384); assert.equal(sheet.truncated,true);
}
assert.equal(output.sheets[0].rows[3][1],'kept');
assert.equal(output.sheets[0].rows[2][1],'');
assert.equal(output.sheets[2].rows.length,0);
assert.equal(output.sheets[3].truncated,false);
`
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("spreadsheet worker bounds: %v\n%s", err, output)
	}
}

func TestSpreadsheetWorkerKeepsASCIIAndBinaryParsing(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node required")
	}
	script := `
const assert = require('node:assert/strict'), fs = require('node:fs'), vm = require('node:vm');
const XLSX = require('../../assets/file-vendor/xlsx.full.min.js');
const workbook = XLSX.utils.book_new();
XLSX.utils.book_append_sheet(workbook, XLSX.utils.aoa_to_sheet([['Name','Count'],['Alpha',42]]), 'Data');
const xlsx = XLSX.write(workbook, {type: 'array', bookType: 'xlsx'});
const utf16 = Buffer.concat([Buffer.from([0xFF, 0xFE]), Buffer.from('Name,City\nJosé,Zürich\n', 'utf16le')]);
const cases = [
 {name:'ascii csv', bytes:Buffer.from('Name,Value\nAlpha,42\n,\nBeta,84\n', 'utf8'), rows:[['Name','Value'],['Alpha','42'],['',''],['Beta','84']]},
 {name:'ascii tsv', bytes:Buffer.from('Name\tValue\nAlpha\t42\n', 'utf8'), rows:[['Name','Value'],['Alpha','42']]},
 {name:'xlsx', bytes:Buffer.from(xlsx), rows:[['Name','Count'],['Alpha','42']]},
 {name:'utf-16le bom', bytes:utf16, rows:[['Name','City'],['José','Zürich']]},
];
for (const {name, bytes, rows: expected} of cases) {
  const input = Uint8Array.from(bytes).buffer;
  let output;
  const context = {XLSX, TextDecoder, importScripts(){}, postMessage(value){output=value}, close(){}};
  vm.createContext(context);
  vm.runInContext(fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8'), context);
  context.onmessage({data:input});
  assert.equal(output.error, undefined, name);
  assert.deepEqual(output.sheets[0].rows, expected, name);
}
`
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("spreadsheet worker ASCII and binary parsing: %v\n%s", err, output)
	}
}

func TestSpreadsheetWorkerDecodesUTF8Text(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node required")
	}
	script := `
const assert = require('node:assert/strict'), fs = require('node:fs'), vm = require('node:vm');
const XLSX = require('../../assets/file-vendor/xlsx.full.min.js');
const cases = [
 {name:'csv', fs:',', text:'Name,City\nJosé,Zürich\n李雷,東京\n'},
 {name:'tsv', fs:'\t', text:'Name\tCity\nJosé\tZürich\n李雷\t東京\n'},
];
for (const {name, text} of cases) {
 for (const bom of ["", String.fromCharCode(0xFEFF)]) {
  const bytes = Buffer.from(bom + text, 'utf8');
  const input = Uint8Array.from(bytes).buffer;
  let output;
  const context = {XLSX, TextDecoder, importScripts(){}, postMessage(value){output=value}, close(){}};
  vm.createContext(context);
  vm.runInContext(fs.readFileSync('../../assets/files/file-spreadsheet-worker.js','utf8'), context);
  context.onmessage({data:input});
  assert.equal(output.error, undefined, name);
  const rows = output.sheets[0].rows;
  assert.deepEqual(rows, [['Name','City'],['José','Zürich'],['李雷','東京']], name + ' bom=' + (bom ? 'yes' : 'no'));
  assert.equal(rows[0][0].charCodeAt(0) === 0xFEFF, false, 'no leading BOM in A1');
 }
}
`
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("spreadsheet worker utf-8 decoding: %v\n%s", err, output)
	}
}
