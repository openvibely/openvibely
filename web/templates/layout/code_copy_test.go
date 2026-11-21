package layout

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestCodeCopyClipboardAndFeedback(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node required")
	}
	source, err := os.ReadFile("base.templ")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, "window.copyCodeText =")
	if start < 0 {
		t.Fatal("copy helpers missing")
	}
	end := strings.Index(text[start:], "// Listen for system theme changes")
	if end < 0 {
		t.Fatal("copy helpers missing")
	}
	script := `const assert = require('node:assert/strict');
const timers = new Map(); let nextTimer = 0;
global.setTimeout = fn => { timers.set(++nextTimer, fn); return nextTimer; };
global.clearTimeout = id => timers.delete(id);
function resetTimers() { const pending = [...timers.values()]; timers.clear(); pending.forEach(fn=>fn()); }
global.window = global;
let restoredFocus = 0, restoredRanges = 0, fallbackCalls = 0, copied = [];
let fallbackOK = true, temporary = [];
const range = {cloneRange(){return this;}};
global.getSelection = () => ({rangeCount:1,getRangeAt:()=>range,removeAllRanges(){},addRange(r){assert.equal(r,range); restoredRanges++;}});
function button() {
 const span = {textContent:'Copy'};
 const path = {setAttribute(k,v){this[k]=v;}};
 return {style:{},disabled:false,span,path,setAttribute(){},querySelector(s){return s==='span'?span:path;},addEventListener(event,fn){this.click=fn;},remove(){this.parent.btn=null;}};
}
global.document = {
 activeElement:{isConnected:true,focus(){restoredFocus++;}},
 body:{appendChild(el){temporary.push(el);}},
 createElement(tag){
  if(tag==='button') return button();
  return {style:{},setAttribute(){},focus(){},select(){},remove(){temporary.splice(temporary.indexOf(this),1);}};
 },
 execCommand(command){assert.equal(command,'copy');fallbackCalls++;copied.push(temporary[0].value);return fallbackOK;}
};
Object.defineProperty(global,'navigator',{value:{clipboard:{writeText:async text=>{copied.push(text);}}},configurable:true});
function pre(text, raw=false) {
 return {btn:null,querySelector(s){return s==='.code-copy-btn'?this.btn:(raw?null:{textContent:text});},appendChild(b){this.btn=b;b.parent=this;},cloneNode(){return {textContent:text,querySelector(){return {remove(){}};}};}};
}
const click = b => b.click({preventDefault(){},stopPropagation(){}});
` + text[start:start+end] + `
(async()=>{
 const first=pre('sudo apt update\n'), second=pre('echo two\n'), raw=pre('plain\n',true);
 const host={querySelectorAll:()=>[first,second,raw]};
 addCodeCopyButtons(host);
 const original=first.btn;
 addCodeCopyButtons(host); assert.equal(first.btn,original,'binding must be idempotent');
 await click(first.btn);
 assert.equal(copied.pop(),'sudo apt update\n');
 assert.equal(first.btn.span.textContent,'Copied');
 assert.equal(second.btn.span.textContent,'Copy','feedback must stay local');
 resetTimers(); assert.equal(first.btn.span.textContent,'Copy');
 const copyPath=first.btn.path.d;
 navigator.clipboard.writeText=async()=>{throw new Error('denied');};
 await click(second.btn);
 assert.equal(copied.pop(),'echo two\n'); assert.equal(second.btn.span.textContent,'Copied');
 assert.equal(fallbackCalls,1); assert.equal(restoredFocus,1); assert.equal(restoredRanges,1); assert.equal(temporary.length,0);
 navigator.clipboard=undefined;
 await click(raw.btn); assert.equal(copied.pop(),'plain\n');
 resetTimers(); assert.equal(second.btn.path.d,copyPath);
 fallbackOK=false;
 await click(first.btn); assert.equal(first.btn.span.textContent,'Copy failed'); assert.equal(first.btn.disabled,false);
 resetTimers(); assert.equal(first.btn.span.textContent,'Copy'); assert.equal(first.btn.path.d,copyPath); assert.equal(temporary.length,0);
 fallbackOK=true;
 await click(first.btn); await click(first.btn); assert.equal(timers.size,1,'repeat clicks replace the reset timer');
 resetTimers();
 const cloned=button(); cloned.span.textContent='Copied'; first.appendChild(cloned);
 addCodeCopyButtons(host); assert.notEqual(first.btn,cloned); assert.equal(first.btn.span.textContent,'Copy');
 await click(first.btn); assert.equal(first.btn.span.textContent,'Copied');
 let finish;
 navigator.clipboard={writeText:()=>new Promise(resolve=>{finish=resolve;})};
 const pending=click(second.btn); assert.equal(second.btn.disabled,true);
 await click(second.btn); finish(); await pending; assert.equal(second.btn.disabled,false);
 resetTimers(); assert.equal(second.btn.span.textContent,'Copy');
})().catch(err=>{console.error(err);process.exit(1);});`
	if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("copy regression: %v\n%s", err, output)
	}
}
