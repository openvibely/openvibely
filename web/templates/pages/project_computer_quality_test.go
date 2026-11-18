package pages

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestComputerQualityUpdatesConnectedViewer(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	source, err := os.ReadFile("project_computer.templ")
	if err != nil {
		t.Fatal(err)
	}
	_, code, ok := strings.Cut(string(source), "function previewQuality(state){")
	if !ok {
		t.Fatal("quality handler missing")
	}
	// Test the connected-viewer path without constructing a network connection.
	end := strings.Index(code, "      disconnect();mode='view';")
	if end < 0 {
		t.Fatal("viewer boundary missing")
	}
	code = code[:end] + "}\n"
	script := `const assert=require('node:assert/strict');let rfb={},syncs=0;
 function syncViewerInput(){syncs++}
 function previewQuality(state){` + code + `
 (async()=>{const same=rfb;
 for(const [quality,expected] of [['low',3],['balanced',7],['high',9],[undefined,7]]){
 await viewer({preview_quality:quality});assert.equal(rfb.qualityLevel,expected);assert.equal(rfb,same);
 }
 assert.equal(syncs,4);
 })().catch(e=>{console.error(e);process.exit(1)});`
	if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("quality regression: %v\n%s", err, output)
	}
}
