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
const context = {XLSX, importScripts(){}, postMessage(value){output=value}, close(){}};
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
