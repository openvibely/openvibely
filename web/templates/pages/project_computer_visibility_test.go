package pages

import (
	"os"
	"os/exec"
	"regexp"
	"testing"
)

func TestComputerVisibilityReconnectsWithoutTakeover(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	source, err := os.ReadFile("project_computer.templ")
	if err != nil {
		t.Fatal(err)
	}
	function := regexp.MustCompile(`function visibilityChanged\(\)[^\n]+`).Find(source)
	if len(function) == 0 {
		t.Fatal("visibility handler missing")
	}
	script := `const assert=require('node:assert/strict');
 let shown=true, dialog={open:true}, closing=false, takeoverRequested=false,
 refreshVersion=0,recording=true,disconnections=0,requests=0,refreshes=0;
 function visible(){return shown}
 function renderDemo(){}
 function disconnect(){disconnections++}
 function refresh(){refreshes++;if(takeoverRequested && dialog.open){requests++;takeoverRequested=false}}
 ` + string(function) + `
 // Returning refreshes the stream without claiming exclusive control.
 shown=false;visibilityChanged();
 assert.equal(disconnections,1);assert.equal(requests,0);
 shown=true;visibilityChanged();assert.equal(requests,0);assert.ok(refreshes>0);
 // A mini preview must never take control.
 dialog.open=false;shown=false;visibilityChanged();
 shown=true;visibilityChanged();assert.equal(requests,0);assert.ok(refreshes>0);
 // Closing fullscreen must not schedule another takeover.
 dialog.open=true;closing=true;shown=false;visibilityChanged();
 assert.equal(takeoverRequested,false);
 `
	output, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("visibility regression: %v\n%s", err, output)
	}
}
