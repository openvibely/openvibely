package pages

import (
	"fmt"
	"github.com/openvibely/openvibely/internal/models"
	templateui "github.com/openvibely/openvibely/web/templates"
	"github.com/openvibely/openvibely/web/templates/components"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBrowserFunctional_SharedTooltips(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><meta charset="utf-8"><body style="--bc:.95 0 0;--ov-menu-surface:#252a33"><button id="native" title="Review changes (⌘⇧D)" aria-describedby="existing">Changes</button><span id="existing">Description</span><button id="state" title="Collapse sidebar (Ctrl+B)" data-tip="Running">State</button><dialog id="dialog"><button id="help" data-model-help="Helpful explanation">Help</button></dialog><canvas id="chart"></canvas>`)
		_ = templateui.Tooltips().Render(r.Context(), w)
		fmt.Fprint(w, `</body></html>`)
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "shared-tooltips", func(b *composerFocusCDP) {
		b.waitFor("tooltip controller ready", `typeof window.openVibelyTooltip`, "object")
		b.evaluate(`var card=document.createElement('div');card.dataset.ovTooltip='Select tasks (Command+click)';card.innerHTML='<button data-kanban-menu-trigger aria-label="More actions"><svg><path/></svg></button>';document.body.appendChild(card);card.dispatchEvent(new PointerEvent('pointerover',{bubbles:true}));'card'`)
		b.waitFor("card selection hint visible", `String(!document.getElementById('ov-shared-tooltip').hidden)`, "true")
		b.evaluate(`var kebab=card.querySelector('button'),icon=kebab.querySelector('path');card.dispatchEvent(new PointerEvent('pointerout',{bubbles:true,relatedTarget:icon}));icon.dispatchEvent(new PointerEvent('pointerover',{bubbles:true}));'kebab'`)
		b.waitFor("kebab suppresses ancestor hint", `String(document.getElementById('ov-shared-tooltip').hidden)`, "true")
		b.evaluate(`kebab.focus();kebab.dispatchEvent(new FocusEvent('focusin',{bubbles:true}));'focus'`)
		b.waitFor("focused kebab has no inherited hint", `String(document.getElementById('ov-shared-tooltip').hidden && kebab.getAttribute('aria-label')==='More actions')`, "true")
		b.evaluate(`card.dispatchEvent(new PointerEvent('pointerover',{bubbles:true,relatedTarget:icon}));'return'`)
		b.waitFor("card hint returns outside kebab", `String(!document.getElementById('ov-shared-tooltip').hidden)`, "true")
		b.evaluate(`kebab.removeAttribute('data-kanban-menu-trigger');kebab.setAttribute('data-tooltip-disabled','');icon.dispatchEvent(new PointerEvent('pointerover',{bubbles:true}));'disabled'`)
		b.waitFor("opted-out close button blocks inherited hint", `String(document.getElementById('ov-shared-tooltip').hidden)`, "true")
		b.evaluate(`kebab.removeAttribute('data-tooltip-disabled');kebab.classList.add('ov-modal-close');kebab.setAttribute('aria-label','Close dialog');kebab.title='Close dialog';icon.dispatchEvent(new PointerEvent('pointerover',{bubbles:true}));kebab.dispatchEvent(new FocusEvent('focusin',{bubbles:true}));'modal-close'`)
		b.waitFor("modal close suppresses own and inherited hints", `String(document.getElementById('ov-shared-tooltip').hidden && kebab.getAttribute('aria-label')==='Close dialog')`, "true")
		b.evaluate(`card.remove();'removed'`)

		b.evaluate(`document.getElementById('native').focus();document.getElementById('native').dispatchEvent(new FocusEvent('focusin',{bubbles:true}));'ready'`)

		b.evaluate(`var control=document.getElementById('native');control.dispatchEvent(new PointerEvent('pointerover',{bubbles:true,clientX:320,clientY:220}));'hover'`)
		b.waitFor("tooltip follows pointer", `document.getElementById('ov-shared-tooltip').style.left`, "332px")
		b.evaluate(`var messages=document.createElement('div');messages.style.cssText='height:40px;overflow:auto';document.body.appendChild(messages);messages.innerHTML='<div style="height:200px">Streaming response</div>';messages.scrollTop=messages.scrollHeight;messages.dispatchEvent(new Event('scroll'));'chunk'`)
		b.waitFor("streaming chat auto-scroll preserves Changes hint", `String(!document.getElementById('ov-shared-tooltip').hidden && document.getElementById('ov-shared-tooltip').textContent==='Review changes⌘⇧D')`, "true")
		b.evaluate(`document.dispatchEvent(new Event('scroll'));'page scroll'`)
		b.waitFor("page scroll still dismisses hint", `String(document.getElementById('ov-shared-tooltip').hidden)`, "true")
		b.evaluate(`messages.remove();control.dispatchEvent(new PointerEvent('pointerover',{bubbles:true,clientX:320,clientY:220}));'hover'`)

		b.evaluate(`document.body.dispatchEvent(new CustomEvent('htmx:beforeSwap',{bubbles:true,detail:{target:document.getElementById('chart')}}));document.getElementById('chart').dispatchEvent(new PointerEvent('pointerout',{bubbles:true}));window.hoverCheck=null;setTimeout(()=>window.hoverCheck=!document.getElementById('ov-shared-tooltip').hidden,500);'waiting'`)
		b.waitFor("unrelated events do not expire hovered tooltip", `String(window.hoverCheck)`, "true")
		b.evaluate(`control.dispatchEvent(new PointerEvent('pointermove',{bubbles:true,clientX:420,clientY:220}));'move'`)
		b.waitFor("moving across wide control follows cursor", `document.getElementById('ov-shared-tooltip').style.left`, "432px")
		b.evaluate(`var cell=document.createElement('div');cell.title='Review changes (⌘⇧D)';document.body.appendChild(cell);control.dispatchEvent(new PointerEvent('pointerout',{bubbles:true,relatedTarget:cell}));cell.dispatchEvent(new PointerEvent('pointerover',{bubbles:true,relatedTarget:control,clientX:430,clientY:220}));'cell'`)
		b.waitFor("same hint transfers without hiding", `String(!document.getElementById('ov-shared-tooltip').hidden && cell.getAttribute('aria-describedby')==='ov-shared-tooltip')`, "true")
		b.evaluate(`control.dispatchEvent(new PointerEvent('pointerover',{bubbles:true,clientX:320,clientY:220}));'back'`)
		b.evaluate(`control.dispatchEvent(new MouseEvent('mousedown',{bubbles:true}));control.dispatchEvent(new FocusEvent('focusin',{bubbles:true}));control.dispatchEvent(new PointerEvent('pointerover',{bubbles:true}));'pressed'`)
		b.waitFor("mousedown dismisses and focus cannot reopen", `String(document.getElementById('ov-shared-tooltip').hidden)`, "true")
		b.evaluate(`document.dispatchEvent(new MouseEvent('mouseup',{bubbles:true}));document.body.dispatchEvent(new PointerEvent('pointerover',{bubbles:true}));control.dispatchEvent(new PointerEvent('pointerover',{bubbles:true}));'reenter'`)
		b.evaluate(`control.dispatchEvent(new MouseEvent('mousedown',{bubbles:true}));cell.dispatchEvent(new PointerEvent('pointerover',{bubbles:true,buttons:1}));'drag'`)
		b.waitFor("drag across controls suppresses hints", `String(document.getElementById('ov-shared-tooltip').hidden)`, "true")
		b.evaluate(`document.dispatchEvent(new MouseEvent('mouseup',{bubbles:true}));var menu=document.createElement('div');menu.className='ov-menu';menu.setAttribute('role','menu');menu.textContent='Actions';document.body.appendChild(menu);cell.dispatchEvent(new PointerEvent('pointerover',{bubbles:true}));'menu'`)
		b.waitFor("open popup suppresses other controls", `String(document.getElementById('ov-shared-tooltip').hidden)`, "true")
		b.evaluate(`document.dispatchEvent(new KeyboardEvent('keydown',{key:'k',ctrlKey:true,bubbles:true}));menu.hidden=true;control.focus();control.dispatchEvent(new FocusEvent('focusin',{bubbles:true}));'shortcut closed menu'`)
		b.waitFor("shortcut-restored focus does not show tooltip", `String(document.getElementById('ov-shared-tooltip').hidden)`, "true")
		b.evaluate(`document.dispatchEvent(new KeyboardEvent('keydown',{key:'Tab',bubbles:true}));control.dispatchEvent(new FocusEvent('focusin',{bubbles:true}));'tab focus'`)
		b.waitFor("deliberate keyboard navigation still shows hint", `String(!document.getElementById('ov-shared-tooltip').hidden)`, "true")
		b.evaluate(`menu.hidden=true;control.dispatchEvent(new PointerEvent('pointerover',{bubbles:true}));'closed menu'`)
		b.waitFor("shortcut badge", `(document.querySelector('#ov-shared-tooltip kbd')?.textContent || '')`, "⌘⇧D")
		b.waitFor("native title suppressed", `String(!document.getElementById('native').hasAttribute('title'))`, "true")
		b.evaluate(`document.getElementById('native').title='Loading models';'updated'`)
		b.waitFor("dynamic title", `document.getElementById('ov-shared-tooltip').textContent`, "Loading models")
		b.evaluate(`document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));'closed'`)
		b.waitFor("description preserved", `document.getElementById('native').getAttribute('aria-describedby')`, "existing")
		b.waitFor("no native tooltip after Escape", `String(!document.getElementById('native').hasAttribute('title'))`, "true")
		b.evaluate(`var fresh=document.createElement('button');fresh.id='fresh';fresh.title='Review changes (Ctrl+Shift+D)';fresh.textContent='Review';document.body.appendChild(fresh);'inserted'`)
		b.waitFor("inserted tooltip adopted", `document.getElementById('fresh').dataset.ovTooltip`, "Review changes (Ctrl+Shift+D)")
		b.evaluate(`document.getElementById('fresh').dispatchEvent(new PointerEvent('pointerover',{bubbles:true}));'hovered'`)
		b.waitFor("Windows shortcut badge", `(document.querySelector('#ov-shared-tooltip kbd')?.textContent || '')`, "Ctrl+Shift+D")
		b.evaluate(`document.body.style.setProperty('--ov-menu-surface','#ffffff');'theme'`)
		b.waitFor("theme follows menu surface", `getComputedStyle(document.getElementById('ov-shared-tooltip')).backgroundColor`, "rgb(255, 255, 255)")
		b.evaluate(`document.getElementById('fresh').remove();'removed'`)
		b.waitFor("removed owner dismisses tooltip", `String(document.getElementById('ov-shared-tooltip').hidden)`, "true")
		b.waitFor("latest label retained", `document.getElementById('native').dataset.ovTooltip`, "Loading models")
		b.evaluate(`document.dispatchEvent(new KeyboardEvent('keydown',{key:'Tab',bubbles:true}));document.getElementById('state').focus();document.getElementById('state').dispatchEvent(new FocusEvent('focusin',{bubbles:true}));'ready'`)
		b.waitFor("custom tooltip", `document.getElementById('ov-shared-tooltip').textContent`, "Running")
		b.evaluate(`document.getElementById('state').dataset.tip='Expand sidebar (Ctrl+B)';'updated'`)
		b.waitFor("sidebar state updates despite adopted title", `document.getElementById('ov-shared-tooltip').textContent`, "Expand sidebarCtrl+B")
		b.evaluate(`window.openVibelyTooltip.clear(document.getElementById('state'));'cleared'`)
		b.waitFor("clearing active tooltip dismisses it", `String(document.getElementById('ov-shared-tooltip').hidden)`, "true")
		b.evaluate(`document.getElementById('native').focus();document.getElementById('native').removeAttribute('title');document.getElementById('native').removeAttribute('data-ov-tooltip');'cleared'`)
		b.waitFor("breadcrumb removal dismisses adopted title", `String(document.getElementById('ov-shared-tooltip').hidden && !document.getElementById('native').hasAttribute('data-ov-tooltip'))`, "true")
		b.evaluate(`document.getElementById('native').dispatchEvent(new PointerEvent('pointerover',{bubbles:true}));'hovered'`)
		b.waitFor("removed tooltip stays absent on hover", `String(document.getElementById('ov-shared-tooltip').hidden)`, "true")

		b.evaluate(`document.getElementById('dialog').showModal();document.getElementById('help').focus();document.getElementById('help').dispatchEvent(new FocusEvent('focusin',{bubbles:true}));'ready'`)
		b.waitFor("modal tooltip", `String(document.getElementById('dialog').contains(document.getElementById('ov-shared-tooltip')) && document.getElementById('ov-shared-tooltip').matches(':popover-open'))`, "true")
		b.evaluate(`document.getElementById('dialog').close();window.openVibelyTooltip.chart({chart:{canvas:document.getElementById('chart')},tooltip:{opacity:1,title:['Tokens'],body:[{lines:['Model: 12']}],footer:['n=3'],caretX:5,caretY:5}});'chart'`)
		b.waitFor("chart callback text", `document.getElementById('ov-shared-tooltip').textContent`, "TokensModel: 12n=3")
		b.evaluate(`window.openVibelyTooltip.chart({chart:{canvas:document.getElementById('chart')},tooltip:{opacity:0}});'closed'`)
		b.waitFor("chart hidden", `String(document.getElementById('ov-shared-tooltip').hidden)`, "true")

		for _, modifier := range []string{"Command", "Ctrl"} {
			b.evaluate(`window.openVibelyTooltip.show(document.getElementById('native'),'Select schedules (` + modifier + `+click)\nMore actions (Right-click)\nSelect or deselect a day (Click)\nSelect days across headers (Click and drag)');'shown'`)
			b.waitFor("schedule gestures follow labels", `String(Array.from(document.querySelectorAll('#ov-shared-tooltip .ov-tooltip-row')).every(row=>row.children.length===2 && row.firstElementChild.tagName==='SPAN' && row.lastElementChild.tagName==='KBD'))`, "true")
			b.waitFor("schedule modifier badge", `(document.querySelector('#ov-shared-tooltip kbd')?.textContent || '')`, modifier+"+click")
		}
		b.evaluate(`var wide=document.createElement('button');wide.title='Switch task (⌘K); Previous/next: ⌘⇧↑/↓; Last visited: ⌘⇧L';wide.style.cssText='position:fixed;left:100px;top:300px;width:500px;height:40px';wide.textContent='Wide control';document.body.appendChild(wide);'ready'`)
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 400, "y": 320}, nil)
		b.waitFor("native pointer placement", `document.getElementById('ov-shared-tooltip').style.left`, "412px")
		b.waitFor("shortcut rows have breathing room", `String((function(){var keys=document.querySelectorAll('#ov-shared-tooltip kbd');return keys.length===3 && keys[1].getBoundingClientRect().top-keys[0].getBoundingClientRect().bottom>=4})())`, "true")
		b.evaluate(`window.longHover=null;setTimeout(()=>window.longHover=!document.getElementById('ov-shared-tooltip').hidden,1200);'waiting'`)
		b.waitFor("stationary native hover remains visible", `String(window.longHover)`, "true")
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 500, "y": 320}, nil)
		b.waitFor("native mouse movement repositions hint", `document.getElementById('ov-shared-tooltip').style.left`, "512px")
	})
}

func TestBrowserFunctional_TaskCardBadgeTooltipContinuity(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><style>.hidden, .dropdown-content { display:none }</style></head><body>`)
		_ = components.TaskCard(models.Task{ID: "badge-test", ParentTaskID: func() *string { id := "parent"; return &id }(), ChainConfig: `{"enabled":true}`, ProjectID: "default", Title: "Task", Status: models.StatusPending, Category: models.CategoryBacklog, HasGoal: true, AgentID: func() *string { id := "model"; return &id }(), SwarmRole: models.SwarmRoleParent}, "default", "", []models.LLMConfig{{ID: "model", Name: "Test model"}}, nil).Render(r.Context(), w)
		_ = templateui.Tooltips().Render(r.Context(), w)
		fmt.Fprint(w, `</body></html>`)
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "card-badges", func(b *composerFocusCDP) {
		b.waitFor("tooltip ready", `typeof window.openVibelyTooltip`, "object")
		b.evaluate(`var badge=Array.from(document.querySelectorAll('.badge')).find(el=>el.textContent.trim()==='Goal');var owner=badge.closest('[data-task-id]');owner.dataset.ovTooltip='Select tasks (Command+click)';owner.dispatchEvent(new PointerEvent('pointerover',{bubbles:true}));'hover'`)
		b.waitFor("card hint visible", `String(!document.getElementById('ov-shared-tooltip').hidden)`, "true")
		for _, label := range []string{"Goal", "Swarm", "Test model", "Chain", "Chained"} {
			b.evaluate(fmt.Sprintf(`var next=Array.from(document.querySelectorAll('.badge')).find(el=>el.textContent.trim()===%q);owner.dispatchEvent(new PointerEvent('pointerout',{bubbles:true,relatedTarget:next}));next.dispatchEvent(new PointerEvent('pointerover',{bubbles:true,relatedTarget:owner}));'badge'`, label))
			b.waitFor(label+" retains card hint", `String(!document.getElementById('ov-shared-tooltip').hidden && owner.getAttribute('aria-describedby')==='ov-shared-tooltip')`, "true")
		}
	})
}

func TestBrowserFunctional_TooltipDelayAndShortcutFocus(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><body><button id="a" data-ov-tooltip="First"><span>Badge</span></button><button id="b" data-ov-tooltip="Second">Second</button><script>
   var clock=1000,jobs=new Map(),sequence=0;
   performance.now=()=>clock;
   window.setTimeout=(fn,delay)=>{jobs.set(++sequence,{fn,at:clock+delay});return sequence};
   window.clearTimeout=id=>jobs.delete(id);
   function advance(ms){clock+=ms;for(var [id,job] of Array.from(jobs)){if(job.at<=clock){jobs.delete(id);job.fn()}}}
  </script>`)
		_ = templateui.Tooltips().Render(r.Context(), w)
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "tooltip-delay", func(b *composerFocusCDP) {
		result := b.evaluate(`(()=>{
   const a=document.getElementById('a'),b=document.getElementById('b'),tip=document.getElementById('ov-shared-tooltip');
   function check(ok,message){if(!ok)throw Error(message)}
   function over(el){el.dispatchEvent(new PointerEvent('pointerover',{bubbles:true}))}
   function out(el,next){el.dispatchEvent(new PointerEvent('pointerout',{bubbles:true,relatedTarget:next}))}
   over(a);advance(399);check(tip.hidden,'initial delay');
   out(a,a.firstElementChild);over(a.firstElementChild);advance(1);check(!tip.hidden,'badge must not restart timer');
   out(a,b);over(b);check(tip.textContent==='Second'&&!tip.hidden,'instant switch');
   out(b,document.body);advance(100);check(tip.hidden,'leave hides');advance(199);over(a);check(!tip.hidden,'warm grace');
   out(a,document.body);advance(301);over(b);check(tip.hidden,'cold again');advance(399);check(tip.hidden,'full delay after reset');advance(1);check(!tip.hidden,'show after reset');
   window.openVibelyTooltip.close();advance(301);over(a);out(a,document.body);advance(500);check(tip.hidden,'cancel abandoned hover');
   over(a);a.dispatchEvent(new MouseEvent('mousedown',{bubbles:true}));document.dispatchEvent(new MouseEvent('mouseup',{bubbles:true}));advance(500);check(tip.hidden,'press cancels pending');
   over(b);b.dispatchEvent(new Event('dragstart',{bubbles:true}));advance(500);check(tip.hidden,'drag cancels pending');
   document.dispatchEvent(new KeyboardEvent('keydown',{key:'d',ctrlKey:true,shiftKey:true,bubbles:true}));b.focus();b.dispatchEvent(new FocusEvent('focusin',{bubbles:true}));advance(1000);check(tip.hidden,'shortcut focus without menu');
   document.dispatchEvent(new KeyboardEvent('keydown',{key:'Tab',bubbles:true}));a.focus();a.dispatchEvent(new FocusEvent('focusin',{bubbles:true}));advance(399);check(tip.hidden,'Tab delay');advance(1);check(!tip.hidden,'Tab shows');
   return 'passed';
  })()`)
		if result != "passed" {
			t.Fatalf("tooltip timing: %s", result)
		}
	})
}

func TestBrowserFunctional_TooltipPointerExit(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><body><button id="owner" title="Helpful tooltip" style="position:fixed;left:100px;top:100px;width:100px;height:40px">Owner</button>`)
		_ = templateui.Tooltips().Render(r.Context(), w)
		fmt.Fprint(w, `</body></html>`)
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "tooltip-pointer-exit", func(b *composerFocusCDP) {
		b.waitFor("controller ready", `typeof window.openVibelyTooltip`, "object")
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 150, "y": 120}, nil)
		b.waitFor("tooltip shown", `String(!document.getElementById('ov-shared-tooltip').hidden)`, "true")
		// Enter the tooltip from its owner, crossing its nested text and row nodes.
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 180, "y": 155}, nil)
		b.waitFor("pointer over tooltip", `String(document.getElementById('ov-shared-tooltip').matches(':hover'))`, "true")
		b.evaluate(`document.getElementById("owner").title="Updated tooltip";'updated'`)
		b.waitFor("tooltip updated under pointer", `document.getElementById("ov-shared-tooltip").textContent`, "Updated tooltip")
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 400, "y": 300}, nil)
		b.waitFor("tooltip closes after leaving both surfaces", `String(document.getElementById('ov-shared-tooltip').hidden)`, "true")
	})
}

func TestBrowserFunctional_TooltipHelpClickAndDialogClose(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><body><button id="help" data-model-help="Help text">Help</button><dialog id="dialog"><button id="pending-help" title="Delayed help">Dialog help</button></dialog>`)
		_ = templateui.Tooltips().Render(r.Context(), w)
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "tooltip-help-lifecycle", func(b *composerFocusCDP) {
		b.waitFor("controller", `typeof window.openVibelyTooltip`, "object")
		b.click("#help")
		b.waitFor("mouse click pins help", `String(!document.getElementById('ov-shared-tooltip').hidden)`, "true")
		b.click("#help")
		b.waitFor("second mouse click closes help", `String(document.getElementById('ov-shared-tooltip').hidden)`, "true")
		b.evaluate(`document.getElementById('help').blur();document.dispatchEvent(new KeyboardEvent('keydown',{key:'Tab',bubbles:true}));var dialog=document.getElementById('dialog');dialog.showModal();document.getElementById('pending-help').dispatchEvent(new FocusEvent('focusin',{bubbles:true}));dialog.close();window.dialogTooltipCheck=null;setTimeout(()=>window.dialogTooltipCheck=document.getElementById('ov-shared-tooltip').hidden,600);'closed'`)
		b.waitFor("closing dialog cancels pending hint", `String(window.dialogTooltipCheck)`, "true")
	})
}

func TestBrowserFunctional_TooltipHiddenOwner(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><body><style>.concealed{display:none}</style><div id="parent"><button id="owner" title="Helpful hint">Owner</button></div>`)
		_ = templateui.Tooltips().Render(r.Context(), w)
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "tooltip-hidden-owner", func(b *composerFocusCDP) {
		b.waitFor("controller", `typeof window.openVibelyTooltip`, "object")
		for _, hide := range []string{`parent.hidden=true`, `parent.style.display='none'`, `parent.className='concealed'`, `parent.style.visibility='hidden'`, `parent.inert=true`} {
			b.evaluate(`var parent=document.getElementById('parent'),owner=document.getElementById('owner'),tip=document.getElementById('ov-shared-tooltip');parent.hidden=false;parent.inert=false;parent.style.cssText='';parent.className='';owner.dispatchEvent(new PointerEvent('pointerover',{bubbles:true}));` + hide + `;window.hiddenCheck=null;setTimeout(()=>hiddenCheck=tip.hidden,500);'pending'`)
			b.waitFor("hidden parent cancels pending hint: "+hide, `String(hiddenCheck)`, "true")
			b.evaluate(`parent.hidden=false;parent.inert=false;parent.style.cssText='';parent.className='';owner.dispatchEvent(new PointerEvent('pointerover',{bubbles:true}));'visible'`)
			b.waitFor("hint can reopen", `String(!tip.hidden)`, "true")
			b.evaluate(hide + `;'hidden'`)
			b.waitFor("hidden parent dismisses visible hint: "+hide, `String(tip.hidden && !owner.hasAttribute('aria-describedby'))`, "true")
		}
	})
}
