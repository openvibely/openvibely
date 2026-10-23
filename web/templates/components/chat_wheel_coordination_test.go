package components

import (
	"os/exec"
	"testing"
)

func TestTranscriptWheelCoordinatesWithLayout(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("requires node")
	}
	script := `
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const source = fs.readFileSync('../../assets/chat-virtual-list.js','utf8');
for (const ua of ['AppleWebKit Safari', 'AppleWebKit Chrome Edg/155']) {
 class Element {
  constructor() { this.listeners = {}; this.style = {}; this.scrollHeight = 100000; this.clientHeight = 500; this.top = 0; this.isConnected = true; }
  get scrollTop() { return this.top; }
  set scrollTop(value) { this.top = Math.round(value); }
  addEventListener(name, callback) { this.listeners[name] = callback; }
  removeEventListener(name) { delete this.listeners[name]; }
 }
 const messages = new Element(), documentEvents = {};
 const context = {Element, navigator:{userAgent:ua}, window:{addEventListener(){}},
  document:{addEventListener:(name,fn)=>documentEvents[name]=fn,querySelectorAll:()=>[messages]},
  MutationObserver:class {observe(){} disconnect(){}},
  requestAnimationFrame:()=>1,cancelAnimationFrame(){},getComputedStyle:el=>({lineHeight:'20',...el.style})};
 vm.runInNewContext(source,context);
 documentEvents['htmx:afterSwap']();
 function wheel(overrides={}) {
  const event={isTrusted:true,cancelable:true,defaultPrevented:false,deltaY:-2.4,deltaX:0,deltaMode:0,target:messages,
   preventDefault(){this.defaultPrevented=true;},...overrides};
  messages.listeners.wheel?.(event);
  return event;
 }
 messages.scrollTop=160;
 for(let i=0;i<100;i++) {
  // History insertion changes the scroll origin in the middle of a gesture.
  if(i===30) messages.scrollTop+=30000;
  assert(wheel().defaultPrevented,ua+': transcript wheel left to asynchronous scrolling');
 }
 assert(Math.abs(messages.scrollTop-29920)<=1,ua+': input lost across layout compensation');
 const tool=new Element();tool.parentElement=messages;tool.style.overflowY='auto';tool.scrollTop=100;
 assert(!wheel({target:tool}).defaultPrevented,'nested scrolling must remain native');
 tool.style.overscrollBehaviorY='contain';tool.scrollTop=0;
 assert(!wheel({target:tool}).defaultPrevented,'contained nested boundary must remain native');
 for(const overrides of [{ctrlKey:true},{metaKey:true},{shiftKey:true},{deltaX:40},{isTrusted:false},{cancelable:false},{deltaY:0}])
  assert(!wheel(overrides).defaultPrevented,'modified/nonvertical input intercepted');
 messages.isConnected=false;
 context.document.querySelectorAll=()=>[];
 documentEvents['htmx:afterSwap']();
 assert(!messages.listeners.wheel,'detached view retains wheel listener');
}
`
	if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
}
