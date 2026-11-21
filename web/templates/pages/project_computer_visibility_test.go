package pages

import (
	"os"
	"os/exec"
	"strings"
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
	// Include both multiline handlers, ending at their event registration.
	_, handlers, found := strings.Cut(string(source), "function suspendViewer(){")
	if !found {
		t.Fatal("suspend handler missing")
	}
	handlers, _, found = strings.Cut(handlers, "document.addEventListener('visibilitychange',visibilityChanged);")
	if !found {
		t.Fatal("visibility registration missing")
	}
	script := `const assert=require('node:assert/strict');
 let shown=true, dialog={open:true}, closing=false, takeoverRequested=false,
 refreshVersion=0,recording=true,disconnections=0,requests=0,refreshes=0,
 closed=false,previewDeleted=false,rfb={},hiddenTimer=null;
 const document={hidden:false}, timers=new Map();let nextTimer=0;
 function setTimeout(fn,delay){assert.equal(delay,600000);timers.set(++nextTimer,fn);return nextTimer}
 function clearTimeout(id){timers.delete(id)}
 function expire(){for(const [id,fn] of [...timers]){timers.delete(id);fn()}}
 function viewerShown(){return shown}
 function visible(){return shown&&!document.hidden}
 let recordingPauses=0;function pauseTeaching(){recordingPauses++}
 function renderDemo(){}
 let inputSyncs=0;function syncViewerInput(){inputSyncs++}
 function disconnect(){disconnections++}
 function refresh(){refreshes++;if(takeoverRequested && dialog.open){requests++;takeoverRequested=false}}
 function suspendViewer(){` + handlers + `
 // Both expanded and mini views preserve the stream during brief app switches.
 for(const expanded of [true,false]){
  dialog.open=expanded;document.hidden=true;recording=true;
  const before=disconnections, synced=inputSyncs, refreshed=refreshes, paused=recordingPauses;
  visibilityChanged();
  assert.equal(disconnections,before);assert.equal(timers.size,1);
  assert.equal(recording,false);assert.ok(inputSyncs>synced);assert.ok(recordingPauses>paused);
  visibilityChanged();assert.equal(timers.size,1);
  document.hidden=false;visibilityChanged();
  assert.equal(timers.size,0);assert.equal(hiddenTimer,null);
  assert.equal(disconnections,before);assert.ok(refreshes>refreshed);
  assert.equal(requests,0);
  // Staying hidden until the grace period ends disconnects once.
  document.hidden=true;visibilityChanged();expire();
  assert.equal(disconnections,before+1);assert.equal(hiddenTimer,null);
  document.hidden=false;visibilityChanged();assert.equal(requests,0);
 }
 // Leaving the computer panel disconnects immediately.
 shown=false;const before=disconnections;visibilityChanged();
 assert.equal(disconnections,before+1);assert.equal(timers.size,0);
 shown=true;visibilityChanged();assert.equal(requests,0);
 // Closing fullscreen must not schedule another takeover.
 dialog.open=true;closing=true;shown=false;visibilityChanged();
 assert.equal(takeoverRequested,false);
 // Removed previews ignore late visibility events.
 const stopped=disconnections;closed=true;visibilityChanged();
 closed=false;previewDeleted=true;visibilityChanged();
 assert.equal(disconnections,stopped);
 `
	output, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("visibility regression: %v\n%s", err, output)
	}
}

// The production UI now sends commands only: image capture lives in the helper.
func TestComputerRecordingSaveRetriesWithoutUploadingImages(t *testing.T) {
	source, err := os.ReadFile("project_computer.templ")
	if err != nil {
		t.Fatal(err)
	}
	_, body, ok := strings.Cut(string(source), "find('[data-teach-stop]').onclick=async()=>{")
	if !ok {
		t.Fatal("save handler missing")
	}
	body, _, ok = strings.Cut(body, "     find('[data-teach-discard]')")
	if !ok {
		t.Fatal("save end missing")
	}
	script := `const assert=require('node:assert/strict');let teachBusy=false,teachState={id:'demo'},recording=true,failed=false,calls=[];
 const root={isConnected:false},teach={},teachNote={},renderDemo=()=>{},acceptTeach=()=>{},computerToast=()=>{};
 const queueTeach=fn=>fn();const teachError=()=>{failed=true};let fail=true;
 async function teachRequest(body){calls.push(body);if(fail){fail=false;throw Error('network')};return {recording:{id:'demo',state:'saved'}}}
 const save=async()=>{` + body + `
 (async()=>{await save();assert.equal(failed,true);assert.equal(teachBusy,false);await save();assert.deepEqual(calls,[{action:'save',id:'demo'},{action:'save',id:'demo'}]);assert.equal(teach.hidden,true)})().catch(e=>{console.error(e);process.exit(1)});`
	out, err := exec.Command("node", "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}
