package pages

import (
	"os/exec"
	"testing"
)

// Execute the production handlers with controlled event targets, including cases
// that ordinary keyboard-navigation tests do not exercise (IME and modifiers).
func TestKeyboardAuditGuardsAndComposerCleanup(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node required")
	}
	script := `
const fs=require('fs'), assert=require('assert');
const read=p=>fs.readFileSync(p,'utf8');
const between=(s,a,b)=>s.split(a)[1].split(b)[0];
const event=(extra={})=>Object.assign({key:'Enter',preventDefault(){this.defaultPrevented=true},target:{closest(){return false}}},extra);
let clicks=0;const option={click(){clicks++},focus(){},scrollIntoView(){}};
let body=between(read('task_detail_editors.templ'),"picker.addEventListener('keydown', function(event) {",'\n  });');
let handler=new Function('event','busy','visibleOptions','activeOption',body);
for(const extra of [{isComposing:true},{keyCode:229},{defaultPrevented:true},{metaKey:true}]) handler(event(extra),false,()=>[option],option);
assert.equal(clicks,0); handler(event(),false,()=>[option],option);assert.equal(clicks,1);
body=between(read('../layout/image_gallery.templ'),"dialog.addEventListener('keydown',e=>{",'});');
let zooms=0;handler=new Function('e','zoom','scale','show','index','close',body);
for(const extra of [{metaKey:true},{ctrlKey:true},{altKey:true}]) handler(event({...extra,key:'0',stopPropagation(){}}),()=>zooms++,2,()=>{},0,()=>{});
assert.equal(zooms,0);handler(event({key:'+',shiftKey:true,stopPropagation(){}}),()=>zooms++,2,()=>{},0,()=>{});assert.equal(zooms,1);
body=between(read('task_workspace_script.templ'),"on(panel, 'keydown', function(event) {",'\n\t\t});');
let selectedCount=0;handler=new Function('event','selected','tabNames','selectTab','panel','opener',body);
const panel={dataset:{},querySelector(){return {focus(){}}}};
for(const extra of [{metaKey:true},{ctrlKey:true},{altKey:true},{shiftKey:true}]) handler(event({...extra,key:'ArrowRight',target:{closest(){return {}}}}),'details',['details','schedules'],()=>selectedCount++,panel,{});
assert.equal(selectedCount,0);
body=between(read('../layout/project_tabs.templ'),"var tab = event.target.closest('[data-project-tab]');\n    if (!tab) return;",'\n   });');
handler=new Function('event','list',body);
handler(event({metaKey:true}),{querySelectorAll(){throw Error('modified arrow reached tab navigation')}});
body=between(read('../layout/base.templ'),"if (event.key !== 'Escape' || event.defaultPrevented || event.isComposing) return;",'\n\t\t\t\t\t\t});');
body="if (event.key !== 'Escape' || event.defaultPrevented || event.isComposing) return;"+body;
let cleared=0;
class MockHTMLElement {}
const escapeHandler=new Function('event','document','closeCardFilterDropdown','HTMLElement',body);
handler=(event,doc,close)=>escapeHandler(event,doc,close,MockHTMLElement);
let menuOpen=true,popoverOpen=false,popoverQueries=0;
const doc={querySelector(selector){
 if(selector==='[popover]:popover-open'){
  assert.equal(typeof MockHTMLElement.prototype.showPopover,'function','unsupported popover selector queried');
  popoverQueries++;return popoverOpen;
 }
 return menuOpen;
},querySelectorAll(selector){if(selector.includes('aria-modal') || selector.includes('.dropdown-open')) return [];return [{closest(){return this},_openVibelyClearSelection(){cleared++}}]}};
handler(event({key:'Escape'}),doc,()=>{});assert.equal(cleared,0);
menuOpen=false;handler(event({key:'Escape',defaultPrevented:true}),doc,()=>{});assert.equal(cleared,0);
handler(event({key:'Escape'}),doc,()=>{});assert.equal(cleared,1);assert.equal(popoverQueries,0);
MockHTMLElement.prototype.showPopover=function(){};
popoverOpen=true;handler(event({key:'Escape'}),doc,()=>{});assert.equal(cleared,1);assert.equal(popoverQueries,1);
popoverOpen=false;handler(event({key:'Escape'}),doc,()=>{});assert.equal(cleared,2);assert.equal(popoverQueries,2);
body=between(read('../components/chat_shared.templ'),'// Dispose global modifier listeners when this composer is replaced.','\n\t\t\t\t\t\tfunction safeSubmit()');
let observer,updates=0;const form={isConnected:true};const document=new EventTarget(),window=new EventTarget();document.body={};
class Observer{constructor(fn){this.fn=fn;observer=this}observe(){}disconnect(){this.disconnected=true}}
new Function('form','document','window','MutationObserver','isShortcutModifierKey','updatePrimaryActionModifierVisual','consumeShortcutModifierVisual','shortcutModifierHeld',body)(form,document,window,Observer,()=>true,()=>updates++,()=>updates++,false);
document.dispatchEvent(new Event('keydown'));assert.equal(updates,1);
form.isConnected=false;observer.fn();assert(form._modifierAbort.signal.aborted);assert(observer.disconnected);
document.dispatchEvent(new Event('keydown'));document.dispatchEvent(new Event('keyup'));window.dispatchEvent(new Event('blur'));assert.equal(updates,1);
`
	if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("keyboard regression: %v\n%s", err, output)
	}
}
