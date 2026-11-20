package pages

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestComputerFailedActionRestoresViewer(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	source, err := os.ReadFile("project_computer.templ")
	if err != nil {
		t.Fatal(err)
	}
	_, code, ok := strings.Cut(string(source), "async function computerAction(event){")
	if !ok {
		t.Fatal("missing action handler")
	}
	code, _, ok = strings.Cut(code, "root.addEventListener('click',computerAction);")
	if !ok {
		t.Fatal("missing action handler boundary")
	}
	script := `const assert=require('node:assert/strict');
 let pending=false,refreshVersion=0,connected=true,refreshes=0,disconnects=0,toasts=[];
 const button={disabled:false,dataset:{computerAction:'login-done'}};const buttons=[button];
 function disconnect(){disconnects++;connected=false}
 function scoped(p){return p}
 function computerToast(message){toasts.push(message)}
 async function refresh(){assert.equal(pending,false);refreshes++;connected=true}
 async function fetch(){assert.equal(connected,true);return {ok:false,json:async()=>({message:'Verification failed'})}}
 async function computerAction(event){` + code + `
 (async()=>{
 await computerAction({target:{closest:()=>button}});
 assert.equal(connected,true);assert.equal(disconnects,0);assert.equal(refreshes,1);assert.equal(button.disabled,false);
 assert.deepEqual(toasts,['Verification failed']);
 })().catch(e=>{console.error(e);process.exitCode=1});`
	if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
}
