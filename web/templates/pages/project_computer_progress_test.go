package pages

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestComputerProgressMessages(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	source, err := os.ReadFile("project_computer.templ")
	if err != nil {
		t.Fatal(err)
	}
	_, expression, ok := strings.Cut(string(source), "const text=startupStage")
	if !ok {
		t.Fatal("progress label missing")
	}
	expression, _, _ = strings.Cut(expression, ";")
	js := `const assert=require('node:assert/strict');
 function label(startupStage,pending=false,seconds=0,everConnected=false,retained=false){return startupStage` + expression + `;}
 assert.equal(label('building',true,4),'Building computer image…');
 assert.equal(label('starting'),'Starting computer…');
 assert.equal(label('',true),'Starting computer…');
 assert.equal(label(''),'Connecting to computer…');
 assert.equal(label('',false,0,true),'Reconnecting to computer…');
 assert.equal(label('',false,2,true),'Retrying in 2 seconds…');`
	if out, err := exec.Command(node, "-e", js).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}
