package pages

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func TestComputerErrorToastReportsAlert(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	var html bytes.Buffer
	if err := ComputerErrorReporter().Render(t.Context(), &html); err != nil {
		t.Fatal(err)
	}
	_, script, ok := strings.Cut(html.String(), "<script>")
	if !ok {
		t.Fatal("missing script")
	}
	script, _, _ = strings.Cut(script, "</script>")
	js := `const assert=require('node:assert/strict');let requests=[],toasts=[],fail=false,now=100000;
 Date.now=()=>now;global.window={showToast:(...args)=>toasts.push(args)};
 global.fetch=async(url,options)=>{requests.push({url,...options});return {ok:!fail}};
 console.error=()=>{};
 ` + script + `
 (async()=>{
 const emit=window.computerErrorToast;
 emit('project','task','failure','error');emit('project','task','failure','error');
 assert.equal(toasts.length,2);assert.equal(requests.length,1);
 assert.equal(requests[0].url,'/projects/project/computer/error?task_id=task');
 assert.equal(JSON.parse(requests[0].body).message,'failure');
 emit('project','task','success','success');assert.equal(requests.length,1);
 emit('other','task','failure','error');assert.equal(requests.length,2);
 now+=60001;emit('project','task','failure','error');assert.equal(requests.length,3);
 fail=true;emit('project','','retry','error');await new Promise(r=>setImmediate(r));
 fail=false;emit('project','','retry','error');assert.equal(requests.length,5);
 })().catch(e=>{process.stderr.write(String(e));process.exitCode=1});`
	if out, err := exec.Command(node, "-e", js).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}
