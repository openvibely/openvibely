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

func TestComputerRecordingUploadsAllCapturesInBatches(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	source, err := os.ReadFile("project_computer.templ")
	if err != nil {
		t.Fatal(err)
	}
	extract := func(start, end string) string {
		_, rest, ok := strings.Cut(string(source), start)
		if !ok {
			t.Fatalf("missing %s", start)
		}
		body, _, ok := strings.Cut(rest, end)
		if !ok {
			t.Fatalf("missing %s", end)
		}
		return start + body
	}
	script := `const assert=require('node:assert/strict');
 let recording=true,teachBusy=false,currentState={},teachSteps=100,teachImages=32;
 let steps=[],images=[],pendingTeachBatch=null,teachState={id:'demo',revision:0};
 const inputActive=()=>true,renderDemo=()=>{},computerUUID=()=>String(++uuid);let uuid=0;
 const retained=false,screen={querySelector:()=>({width:1280,height:720})};
 const document={createElement:()=>({getContext:()=>({drawImage(){}}),toDataURL:()=> 'data:image/jpeg;base64,'+'A'.repeat(1024*1024)})};
 let attempts=[],savedSteps=[],savedImages=[],fail=true;
 async function teachRequest(batch){
  attempts.push(JSON.parse(JSON.stringify(batch)));
  assert.ok(JSON.stringify(batch).length<28*1024*1024);
  if(fail){fail=false;throw Error('network interruption')}
  assert.equal(batch.revision,teachState.revision);
  savedSteps.push(...batch.steps);savedImages.push(...batch.images);
  return {recording:{id:'demo',revision:batch.revision+1}};
 }
 function acceptTeach(data){teachState=data.recording}
 ` + extract("function addStep(text)", "     let teachFlushing") + extract("async function flushTeach(){", "     function teachError") + `
 (async()=>{
 for(let i=0;i<120;i++)addStep('Action '+i);
 for(let i=0;i<40;i++)assert.equal(captureScreen('Frame '+i),true);
 assert.equal(recording,true);assert.equal(steps.length,160);assert.equal(images.length,40);
 await assert.rejects(flushTeach(),/network interruption/);
 assert.equal(steps.length,160);assert.equal(images.length,40);
 const retry=JSON.stringify(pendingTeachBatch);
 recording=false;await flushTeach();
 assert.equal(JSON.stringify(attempts[1]),retry);
 assert.equal(savedSteps.length,160);assert.equal(savedImages.length,40);
 assert.equal(steps.length,0);assert.equal(images.length,0);assert.equal(pendingTeachBatch,null);
 assert.equal(recording,false);assert.ok(attempts.length>2);
 })().catch(error=>{console.error(error);process.exit(1)});
 `
	cmd := exec.Command(node, "-e", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("recording batching: %v\n%s", err, out)
	}
}
