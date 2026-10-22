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
const listeners = {}, streams = [];
global.document = {addEventListener:(name,fn)=>listeners[name]=fn};
global.window = {addEventListener:(name,fn)=>listeners[name]=fn};
function stream(args) { const s={args,closed:0,close(){this.closed++}};streams.push(s);return s; }
window.openVibelyThreadCache={execution:(...args)=>stream(args)};
window.openVibelyLive={execution:(...args)=>stream(args)};
const assert=(ok,msg)=>{if(!ok)throw Error(msg)};
`
	checks := `
const controller=window.openVibelyThreadController;
const first={isConnected:true}, second={isConnected:true};
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
delete window.openVibelyThreadCache;
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
