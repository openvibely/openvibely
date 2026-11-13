package components

import (
	"os"
	"os/exec"
	"testing"
)

func TestWindowedThreadResumesWithoutFullSnapshot(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node required")
	}
	source, err := os.ReadFile("../../assets/thread-snapshots.js")
	if err != nil {
		t.Fatal(err)
	}
	harness := `
const assert=(v,m)=>{if(!v)throw Error(m)}, calls=[];
global.document={addEventListener(){},getElementById(){return null}};
global.window={addEventListener(){},openVibelyLive:{execution(...args){calls.push(args);return {close(){}}}}};
global.location={search:''};global.setInterval=()=>0;
global.fetch=async()=>({ok:true,json:async()=>[{id:'execution',project_id:'project',task_id:'task',status:'running'}]});
`
	checks := `
(async()=>{
const cache=window.openVibelyThreadCache;
cache.execution('execution','project','visible suffix',{getAttribute:n=>n==='data-output-window'?'cursor':null},9000000);
assert(calls.length===1&&calls[0][1]===9000000,'must resume from absolute offset');
assert(cache.runs.size===0,'must not cache suffix as complete history');
await cache.discover();
assert(calls.length===1,'background discovery must not replay omitted history');
})().catch(e=>{console.error(e);process.exitCode=1});`
	if out, err := exec.Command(node, "-e", harness+string(source)+checks).CombinedOutput(); err != nil {
		t.Fatalf("windowed cache: %v\n%s", err, out)
	}
}
