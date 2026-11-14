package components

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestThreadControllerViewLifecycle(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node required")
	}
	var buf bytes.Buffer
	if err := ThreadControllerScript().Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	source := html[strings.Index(html, "<script>")+len("<script>") : strings.LastIndex(html, "</script>")]
	harness := `
const listeners = {}, streams = [], cacheCalls = [], liveCalls = [], suppressed = [];
global.document = {addEventListener:(name,fn)=>listeners[name]=fn};
global.window = {addEventListener:(name,fn)=>listeners[name]=fn};
function stream(args) { const s={args,closed:0,close(){this.closed++}};streams.push(s);return s; }
window.openVibelyThreadCache={
 execution:(...args)=>{cacheCalls.push(args);return stream(args)},
 suppressReplay:(...args)=>suppressed.push(args)
};
window.openVibelyLive={execution:(...args)=>{liveCalls.push(args);return stream(args)}};
const assert=(ok,msg)=>{if(!ok)throw Error(msg)};
`
	checks := `
const controller=window.openVibelyThreadController;
const first={isConnected:true,dataset:{}}, second={isConnected:true,dataset:{}};
const a=controller.attach('a','project','hé',first,3);
assert(a.args[2]==='hé'&&a.args[4]===3,'seed or UTF8 cursor lost');
const b=controller.attach('a','project','hé!',first,4);
assert(a.closed===1&&b.closed===0,'reattach did not replace view listener');
a.close();assert(b.closed===0,'old close detached current listener');
const c=controller.attach('b','project','',second,0);
first.isConnected=false;listeners['htmx:afterSwap']();
assert(b.closed===1&&c.closed===0,'swap detached a preserved view or leaked a removed one');
listeners['htmx:historyRestore']();assert(b.closed===1,'view close was not idempotent');
controller.detach(second);controller.detach(second);assert(c.closed===1,'explicit detach was not idempotent');
const leases=[0,1,2].map(i=>controller.attach('bulk'+i,'project','',{isConnected:true,dataset:{}},0));
window._chatEventSources=leases.slice();
window._threadEventSources=leases.slice();
window._chatEventSourceByExec={bulk0:leases[0],bulk1:leases[1],bulk2:leases[2]};
const replacement={};window._chatEventSourceByExec.bulk0=replacement;
window._chatEventSources.slice().forEach(s=>s.close());
assert(leases.every(s=>s.closed===1),'bulk cleanup skipped a lease');
assert(window._chatEventSources.length===0&&window._threadEventSources.length===0,'legacy arrays retain closed leases');
assert(Object.keys(window._chatEventSourceByExec).length===1&&window._chatEventSourceByExec.bulk0===replacement,'close removed replacement or retained old lease');
// A short display seed must resume at the full transcript's byte offset.
const bounded={isConnected:true,dataset:{outputWindow:'9000:live-10000-digest'}};
const cachedBefore=cacheCalls.length, liveBefore=liveCalls.length;
const boundedLease=controller.attach('bounded','project','short tail',bounded,10000);
assert(cacheCalls.length===cachedBefore,'bounded output entered full-source cache');
assert(liveCalls.length===liveBefore+1&&boundedLease.args.join(',')==='bounded,10000,project','bounded output replayed from seed length instead of absolute offset');
assert(JSON.stringify(suppressed)==='[["bounded","project"]]','bounded output did not suppress background replay');
const reopened=controller.attach('bounded','project','new tail',bounded,10020);
assert(boundedLease.closed===1&&reopened.closed===0,'bounded reattach leaked old subscription');
assert(reopened.args.join(',')==='bounded,10020,project'&&cacheCalls.length===cachedBefore,'bounded reopen lost offset or used full-source cache');
bounded.isConnected=false;listeners['htmx:afterSwap']();
assert(reopened.closed===1,'removed bounded view leaked subscription');
assert(suppressed.length===2,'bounded reattach did not suppress replay');
// Bounded views also work when the optional snapshot cache is unavailable.
delete window.openVibelyThreadCache;
bounded.isConnected=true;
const uncached=controller.attach('bounded','project','tail',bounded,10040);
assert(uncached.args.join(',')==='bounded,10040,project','uncached bounded offset changed');
controller.detach(bounded);assert(uncached.closed===1,'uncached bounded view did not detach');
const fallback=controller.attach('fallback','project','seed',second,4);
assert(fallback.args.join(',')==='fallback,4,project','transport fallback offset changed');
listeners.pagehide();assert(fallback.closed===1,'pagehide leaked listener');
assert(controller.render===window.renderLiveChatContent&&controller.restore===window.restoreChatTranscriptScroll&&controller.schedule===window.scheduleChatContentRender,'compatibility globals have separate owners');
`
	// Initializing another page must not replace the controller or lose its leases.
	script := harness + source + "\nconst owner=window.openVibelyThreadController;\n" + source + "\nassert(owner===window.openVibelyThreadController,'controller replaced on page initialization');\n" + checks
	if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("controller lifecycle: %v\n%s", err, output)
	}
}
