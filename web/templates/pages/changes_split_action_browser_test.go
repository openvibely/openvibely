package pages

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/models"
	"net/http"
	"strings"
	"testing"
)

func TestBrowserFunctional_ChangesSplitActionPreferencesAndOneClick(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := worktreeActionsFixture(t, "", nil)
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/", "changes-split-action", func(b *composerFocusCDP) {
		b.waitFor("upstream default ready", `String(document.querySelector('[data-changes-primary]')?.textContent==='Create PR' && !document.querySelector('[data-changes-primary]').matches(':disabled,[aria-disabled="true"]'))`, "true")
		b.waitFor("repository names without remote labels", `JSON.stringify(Array.from(document.querySelector('[data-changes-repo]').options,option=>({label:option.textContent,value:option.value})))`, `[{"label":"upstream/repo","value":"https://github.com/upstream/repo"},{"label":"me/repo","value":"https://github.com/me/repo"}]`)
		b.evaluate(`document.addEventListener('htmx:afterRequest',()=>fetch('/fixture-state').then(r=>r.json()).then(s=>window.actionState=s));'ok'`)
		b.click(`[data-changes-primary]`)
		b.waitFor("PR submitted once", `String(window.actionState?.actions.length===1&&window.actionState.actions[0].base==='main'&&window.actionState.actions[0].target_repo==='https://github.com/upstream/repo')`, "true")
		b.click(`[data-changes-settings-toggle]`)
		b.waitFor("settings open", `String(document.querySelector('#changes-split-settings').matches(':popover-open'))`, "true")
		b.click(`[data-changes-mode="local"]`)
		b.waitFor("local default saved", `String(document.querySelector('[data-changes-primary]').textContent==='Merge'&&!document.querySelector('[data-changes-primary]').matches(':disabled,[aria-disabled="true"]'))`, "true")
		b.evaluate(`(()=>{const branch=document.querySelector('[data-changes-local-branch]');branch.value='develop';branch.dispatchEvent(new Event('change',{bubbles:true}));const method=document.querySelector('[data-changes-method]');method.value='squash';method.dispatchEvent(new Event('change',{bubbles:true}));return 'ok';})()`)
		b.waitFor("squash preference saved", `String(document.querySelector('[data-changes-primary]').textContent==='Squash merge'&&!document.querySelector('[data-changes-primary]').matches(':disabled,[aria-disabled="true"]'))`, "true")
		b.clickMenuBackdrop(`[data-changes-settings-toggle]`)
		b.click(`[data-changes-primary]`)
		b.waitFor("local action dispatched once", `String(window.actionState?.actions.length===2&&window.actionState.actions[1].merge_type==='squash'&&window.actionState.actions[1].changes_target==='develop')`, "true")
		b.evaluate(`window.oldSplit=document.querySelector('[data-changes-split]');htmx.ajax('GET','/tasks/layer-task/changes',{target:'#changes-content',swap:'innerHTML'});'ok'`)
		b.waitFor("saved action restored after refresh", `String(document.querySelector('[data-changes-split]')!==window.oldSplit&&document.querySelector('[data-changes-primary]').textContent==='Squash merge'&&!document.querySelector('[data-changes-primary]').matches(':disabled,[aria-disabled="true"]'))`, "true")
		for _, selector := range []string{"[data-changes-primary]", "[data-changes-settings-toggle]"} {
			raw := b.evaluate(`(()=>{const r=document.querySelector('` + selector + `').getBoundingClientRect();return JSON.stringify({x:r.x+r.width/2,y:r.y+r.height/2});})()`)
			var point struct {
				X float64 `json:"x"`
				Y float64 `json:"y"`
			}
			if err := json.Unmarshal([]byte(raw), &point); err != nil {
				t.Fatal(err)
			}
			b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": point.X, "y": point.Y}, nil)
			b.call("Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": point.X, "y": point.Y, "button": "left", "buttons": 1, "clickCount": 1}, nil)
			b.waitFor("split hit area stays fixed", `(()=>{const root=document.querySelector('.ov-changes-split');return String(getComputedStyle(root).transform==='none'&&Array.from(root.querySelectorAll('.btn')).every(el=>getComputedStyle(el).transform==='none'));})()`, "true")
			// Release outside to avoid performing the action during this visual check.
			b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 1, "y": 1, "buttons": 1}, nil)
			b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": 1, "y": 1, "button": "left", "buttons": 0, "clickCount": 1}, nil)
		}
		b.waitFor("button typography matches view controls", `(()=>{const a=getComputedStyle(document.querySelector('[data-changes-primary]')),v=getComputedStyle(document.querySelector('#diff-btn-inline'));return String(a.fontFamily===v.fontFamily&&a.fontSize===v.fontSize&&a.fontWeight===v.fontWeight&&a.height===v.height);})()`, "true")
	})
}

func TestBrowserFunctional_ChangesSplitActionPartialDiscovery(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := worktreeActionsFixture(t, "", nil)
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/", "changes-partial-discovery", func(b *composerFocusCDP) {
		b.waitFor("initial settings", `String(document.querySelector('[data-changes-primary]')?.matches(':disabled,[aria-disabled="true"]')===false)`, "true")
		b.evaluate(`(()=>{const original=window.fetch;window.fetch=async function(url,options){const response=await original(url,options);if(String(url).endsWith('/action-settings')&&(!options?.method||options.method==='GET')){const data=await response.json();data.repositories=data.repositories.filter(r=>!r.upstream);data.github_error='Upstream repository is unavailable';return new Response(JSON.stringify(data),{status:200,headers:{'Content-Type':'application/json'}});}return response;};window.oldSplit=document.querySelector('[data-changes-split]');htmx.ajax('GET','/tasks/layer-task/changes',{target:'#changes-content',swap:'innerHTML'});return 'ok';})()`)
		b.waitFor("unavailable upstream retained", `String(document.querySelector('[data-changes-split]')!==window.oldSplit&&document.querySelector('[data-changes-primary]')?.matches(':disabled,[aria-disabled="true"]')&&document.querySelector('[data-changes-repo] option:checked')?.textContent.includes('Unavailable'))`, "true")
		b.click(`[data-changes-settings-toggle]`)
		b.evaluate(`(()=>{const repo=document.querySelector('[data-changes-repo]');repo.value='https://github.com/me/repo';repo.dispatchEvent(new Event('change',{bubbles:true}));return 'ok';})()`)
		b.waitFor("fork action enabled despite warning", `String(!document.querySelector('[data-changes-primary]').matches(':disabled,[aria-disabled="true"]')&&document.querySelector('[data-changes-primary]').textContent==='Create PR'&&!document.querySelector('[data-changes-error]').hidden)`, "true")
		b.waitFor("PR destination retained in tooltip and accessible label", `(()=>{const el=document.querySelector('[data-changes-primary]');return String((el.title||el.dataset.ovTooltip)==='me/repo:main'&&el.getAttribute('aria-label')==='Create PR (me/repo:main)');})()`, "true")
		b.clickMenuBackdrop(`[data-changes-settings-toggle]`)
		b.evaluate(`document.addEventListener('htmx:afterRequest',()=>fetch('/fixture-state').then(r=>r.json()).then(s=>window.partialActions=s));'ok'`)
		b.click(`[data-changes-primary]`)
		b.waitFor("fork PR submitted", `String(window.partialActions?.actions.length===1&&window.partialActions.actions[0].target_repo==='https://github.com/me/repo')`, "true")
	})
}

func TestBrowserFunctional_ChangesSplitActionRebaseTarget(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := worktreeActionsFixture(t, "", nil)
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/", "changes-rebase-target", func(b *composerFocusCDP) {
		b.waitFor("settings loaded", `String(document.querySelector('[data-changes-primary]')?.matches(':disabled,[aria-disabled="true"]')===false)`, "true")
		b.click(`[data-changes-settings-toggle]`)
		b.click(`[data-changes-mode="local"]`)
		b.waitFor("main has no rebase", `String(document.querySelector('[data-changes-method] option[value="rebase"]')?.disabled===true)`, "true")
		b.evaluate(`(()=>{const el=document.querySelector('[data-changes-local-branch]');el.value='develop';el.dispatchEvent(new Event('change'));return 'ok';})()`)
		b.waitFor("develop allows rebase", `String(document.querySelector('[data-changes-method] option[value="rebase"]')?.disabled===false)`, "true")
		b.evaluate(`(()=>{const el=document.querySelector('[data-changes-method]');el.value='rebase';el.dispatchEvent(new Event('change'));return 'ok';})()`)
		b.waitFor("rebase enabled", `String(document.querySelector('[data-changes-primary]').textContent==='Rebase'&&!document.querySelector('[data-changes-primary]').matches(':disabled,[aria-disabled="true"]'))`, "true")
		b.evaluate(`(()=>{const el=document.querySelector('[data-changes-local-branch]');el.value='main';el.dispatchEvent(new Event('change'));return 'ok';})()`)
		b.waitFor("main disables saved rebase", `String(document.querySelector('[data-changes-method] option[value="rebase"]').disabled&&document.querySelector('[data-changes-primary]').matches(':disabled,[aria-disabled="true"]'))`, "true")
		b.evaluate(`(()=>{const el=document.querySelector('[data-changes-local-branch]');el.value='develop';el.dispatchEvent(new Event('change'));return 'ok';})()`)
		b.waitFor("rebase restored", `String(!document.querySelector('[data-changes-primary]').matches(':disabled,[aria-disabled="true"]'))`, "true")
		b.evaluate(`(()=>{const original=window.fetch;window.fetch=async function(url,options){const response=await original(url,options);if(String(url).endsWith('action-local?target=main'))await new Promise(resolve=>window.releaseMainEligibility=resolve);return response;};const el=document.querySelector('[data-changes-local-branch]');el.value='main';el.dispatchEvent(new Event('change'));return 'ok';})()`)
		b.waitFor("old request held", `String(typeof window.releaseMainEligibility==='function')`, "true")
		b.evaluate(`(()=>{const el=document.querySelector('[data-changes-local-branch]');el.value='develop';el.dispatchEvent(new Event('change'));return 'ok';})()`)
		b.waitFor("new selection checked", `String(!document.querySelector('[data-changes-primary]').matches(':disabled,[aria-disabled="true"]'))`, "true")
		b.evaluate(`window.releaseMainEligibility();'ok'`)
		b.evaluate(`document.addEventListener('htmx:afterRequest',()=>fetch('/fixture-state').then(r=>r.json()).then(s=>window.actionState=s));'ok'`)
		b.clickMenuBackdrop(`[data-changes-settings-toggle]`)
		b.click(`[data-changes-primary]`)
		b.waitFor("rebase submitted to develop", `String(window.actionState?.actions.some(a=>a.changes_target==='develop'&&a.merge_type==='rebase'))`, "true")
	})
}

func TestBrowserFunctional_ChangesSplitActionMissingTarget(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := worktreeActionsFixture(t, "", nil)
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/", "changes-missing-target", func(b *composerFocusCDP) {
		b.waitFor("settings ready", `String(document.querySelector('[data-changes-primary]')?.matches(':disabled,[aria-disabled="true"]')===false)`, "true")
		b.evaluate(`(()=>{const original=window.fetch;window.fetch=async function(url,options){const response=await original(url,options);if(String(url).endsWith('/action-settings')&&(!options?.method||options.method==='GET')){const data=await response.json();data.preferences.mode='local';data.preferences.local_branch='deleted';return new Response(JSON.stringify(data),{status:200});}if(String(url).endsWith('action-local?target=deleted'))return new Response(JSON.stringify({message:'local target branch no longer exists'}),{status:400});return response;};window.oldSplit=document.querySelector('[data-changes-split]');htmx.ajax('GET','/tasks/layer-task/changes',{target:'#changes-content',swap:'innerHTML'});return 'ok';})()`)
		b.waitFor("missing target disabled action", `String(document.querySelector('[data-changes-split]')!==window.oldSplit&&document.querySelector('[data-changes-primary]').textContent==='Merge'&&document.querySelector('[data-changes-primary]').matches(':disabled,[aria-disabled="true"]'))`, "true")
		b.click(`[data-changes-settings-toggle]`)
		b.waitFor("local controls accessible", `String(!document.querySelector('[data-changes-mode="local"]').disabled&&!document.querySelector('[data-changes-local-fields]').hidden)`, "true")
		b.evaluate(`(()=>{const el=document.querySelector('[data-changes-local-branch]');el.value='main';el.dispatchEvent(new Event('change'));return 'ok';})()`)
		b.waitFor("replacement eligible", `String(document.querySelector('[data-changes-primary]').textContent==='Merge'&&!document.querySelector('[data-changes-primary]').matches(':disabled,[aria-disabled="true"]'))`, "true")
	})
}

func TestBrowserFunctional_ChangesSplitActionKeepsColorWhileSaving(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := worktreeActionsFixture(t, "", nil)
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/", "changes-stable-color", func(b *composerFocusCDP) {
		b.waitFor("ready", `String(document.querySelector('[data-changes-primary]')?.getAttribute('aria-disabled')==='false')`, "true")
		b.click(`[data-changes-settings-toggle]`)
		b.waitFor("initial color settled", `String(document.querySelector('[data-changes-primary]').getAnimations().length===0)`, "true")
		b.evaluate(`(()=>{window.primaryColor=getComputedStyle(document.querySelector('[data-changes-primary]')).backgroundColor;window.heldRequests=[];const original=window.fetch;window.fetch=async function(url,options){const response=await original(url,options);if(String(url).includes('/action-local?')||(String(url).endsWith('/action-settings')&&options?.method==='POST'))await new Promise(resolve=>window.heldRequests.push(resolve));return response;};return 'ok';})()`)
		b.click(`[data-changes-mode="local"]`)
		b.waitFor("save and check held", `String(window.heldRequests.length===2)`, "true")
		b.waitFor("color stable while blocked", `(()=>{const el=document.querySelector('[data-changes-primary]');return !el.disabled&&el.getAttribute('aria-disabled')==='true'&&el.getAttribute('aria-busy')==='true'&&getComputedStyle(el).backgroundColor===window.primaryColor ? 'true' : JSON.stringify({disabled:el.disabled,aria:el.getAttribute('aria-disabled'),busy:el.getAttribute('aria-busy'),color:getComputedStyle(el).backgroundColor,before:window.primaryColor});})()`, "true")
		b.evaluate(`document.querySelector('[data-changes-primary]').click();fetch('/fixture-state').then(r=>r.json()).then(s=>window.blockedActionCount=s.actions.length);'ok'`)
		b.waitFor("pending action blocked", `String(window.blockedActionCount===0)`, "true")
		b.evaluate(`window.heldRequests.splice(0).forEach(resolve=>resolve());'ok'`)
		b.waitFor("local ready", `String(document.querySelector('[data-changes-primary]').getAttribute('aria-disabled')==='false')`, "true")
		b.click(`[data-changes-settings-toggle]`)
		b.click(`[data-changes-mode="pr"]`)
		b.waitFor("PR save held", `String(window.heldRequests.length===1)`, "true")
		b.waitFor("PR color stable", `(()=>{const el=document.querySelector('[data-changes-primary]');return String(!el.disabled&&el.getAttribute('aria-disabled')==='true'&&getComputedStyle(el).backgroundColor===window.primaryColor);})()`, "true")
		b.evaluate(`window.heldRequests.splice(0).forEach(resolve=>resolve());'ok'`)
		b.waitFor("PR ready", `String(document.querySelector('[data-changes-primary]').getAttribute('aria-disabled')==='false')`, "true")
	})
}

func TestBrowserFunctional_ChangesSplitActionBranchDropdown(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := worktreeActionsFixture(t, "", nil)
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/", "changes-branch-dropdown", func(b *composerFocusCDP) {
		b.waitFor("branches loaded", `(()=>{const el=document.querySelector('[data-changes-branch]');return String(el?.tagName==='SELECT'&&el.value==='main'&&Array.from(el.options).map(o=>o.value).join(',')==='main,develop');})()`, "true")
		b.click(`[data-changes-settings-toggle]`)
		b.evaluate(`(()=>{const el=document.querySelector('[data-changes-branch]');el.value='develop';el.dispatchEvent(new Event('change',{bubbles:true}));return 'ok';})()`)
		b.waitFor("selection saved", `String(document.querySelector('[data-changes-primary]').getAttribute('aria-disabled')==='false'&&document.querySelector('[data-changes-branch]').value==='develop')`, "true")
		b.evaluate(`window.oldSplit=document.querySelector('[data-changes-split]');htmx.ajax('GET','/tasks/layer-task/changes',{target:'#changes-content',swap:'innerHTML'});'ok'`)
		b.waitFor("saved selection and full list restored", `(()=>{const el=document.querySelector('[data-changes-branch]');return String(document.querySelector('[data-changes-split]')!==window.oldSplit&&el?.value==='develop'&&el.options.length===2&&Array.from(el.options).some(o=>o.value==='main'));})()`, "true")
	})
}

func TestBrowserFunctional_ChangesSplitActionRunningSettings(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := worktreeActionsFixture(t, "", nil, models.StatusRunning)
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/", "changes-running-settings", func(b *composerFocusCDP) {
		b.waitFor("PR settings loaded", `String(document.querySelector('[data-changes-repo]')?.options.length>0&&document.querySelector('[data-changes-primary]').disabled)`, "true")
		b.waitFor("both halves disabled", `String(document.querySelector('[data-changes-settings-toggle]').disabled&&document.querySelector('[data-changes-primary]').disabled)`, "true")
		b.evaluate(`document.querySelector('[data-changes-settings-toggle]').click();document.querySelector('[data-changes-primary]').click();fetch('/fixture-state').then(r=>r.json()).then(s=>window.runningActions=s.actions.length);'ok'`)
		b.waitFor("settings stay closed", `String(document.querySelector('[data-changes-settings-toggle]').getAttribute('aria-expanded')==='false')`, "true")
		b.waitFor("no mutations", `String(window.runningActions===0)`, "true")
	})
}

func TestBrowserFunctional_ChangesSplitActionWithoutNativePopover(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := worktreeActionsFixture(t, "", nil)
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/", "changes-legacy-popover", func(b *composerFocusCDP) {
		b.call("Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": `
			window.legacyErrors=[];
			window.addEventListener('error',event=>legacyErrors.push(event.error?.stack||event.message));
			window.addEventListener('unhandledrejection',event=>legacyErrors.push(String(event.reason)));
			HTMLElement.prototype.showPopover=undefined;
			HTMLElement.prototype.hidePopover=undefined;
			const matches=Element.prototype.matches;
			Element.prototype.matches=function(selector){
				if(selector.includes(':popover-open'))throw new SyntaxError('Unsupported popover selector');
				return matches.call(this,selector);
			};
			// Simulate an engine that has no UA rule hiding popover elements.
			document.addEventListener('DOMContentLoaded',()=>{
				const style=document.createElement('style');style.textContent='[popover]{display:block}';document.head.append(style);
			});
		`}, nil)
		b.call("Page.reload", map[string]any{}, nil)
		b.waitFor("legacy settings ready", `String(window.legacyErrors&&document.querySelector('[data-changes-primary]')?.getAttribute('aria-disabled')==='false')`, "true")
		b.waitFor("menu initially hidden", `String(document.querySelector('#changes-split-settings').hidden&&document.querySelector('#changes-split-settings').getClientRects().length===0)`, "true")
		b.click(`[data-changes-settings-toggle]`)
		b.waitFor("fallback menu open", `String(!document.querySelector('#changes-split-settings').hidden&&document.querySelector('[data-changes-settings-toggle]').getAttribute('aria-expanded')==='true')`, "true")
		b.evaluate(`window.toggleChangesShortcut=()=>{const apple=/Mac|iPhone|iPad/.test(navigator.platform);document.activeElement.dispatchEvent(new KeyboardEvent('keydown',{key:'D',code:'KeyD',shiftKey:true,metaKey:apple,ctrlKey:!apple,bubbles:true,cancelable:true}));};window.toggleChangesShortcut();'ok'`)
		b.waitFor("open fallback blocks workspace shortcut", `String(!document.querySelector('#tab-changes').classList.contains('hidden')&&!document.querySelector('#changes-split-settings').hidden)`, "true")
		b.click(`[data-changes-mode="local"]`)
		b.waitFor("fallback controls usable", `String(document.querySelector('[data-changes-primary]').textContent==='Merge'&&document.querySelector('[data-changes-primary]').getAttribute('aria-disabled')==='false')`, "true")
		b.clickMenuBackdrop(`[data-changes-settings-toggle]`)
		b.waitFor("outside click closes fallback", `String(document.querySelector('#changes-split-settings').hidden&&!document.querySelector('#changes-split-settings').hasAttribute('data-menu-open'))`, "true")
		b.click(`[data-changes-settings-toggle]`)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Escape", "code": "Escape", "windowsVirtualKeyCode": 27}, nil)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "Escape", "code": "Escape", "windowsVirtualKeyCode": 27}, nil)
		b.waitFor("escape closes and restores focus", `String(document.querySelector('#changes-split-settings').hidden&&document.activeElement===document.querySelector('[data-changes-settings-toggle]'))`, "true")
		b.evaluate(`window.toggleChangesShortcut();'ok'`)
		b.waitFor("closed fallback releases workspace shortcut", `String(document.querySelector('#tab-changes').classList.contains('hidden'))`, "true")
		b.evaluate(`window.toggleChangesShortcut();'ok'`)
		b.waitFor("workspace shortcut returns to changes", `String(!document.querySelector('#tab-changes').classList.contains('hidden'))`, "true")
		b.click(`[data-changes-settings-toggle]`)
		b.evaluate(`window.oldSplit=document.querySelector('[data-changes-split]');htmx.ajax('GET','/tasks/layer-task/changes',{target:'#changes-content',swap:'innerHTML'});'ok'`)
		b.waitFor("swap closes menu and restores settings", `String(document.querySelector('[data-changes-split]')!==window.oldSplit&&document.querySelector('#changes-split-settings').hidden&&document.querySelector('[data-changes-primary]').getAttribute('aria-disabled')==='false')`, "true")
		b.waitFor("backdrop released", `String(!document.querySelector('#ov-menu-backdrop')||getComputedStyle(document.querySelector('#ov-menu-backdrop')).display==='none')`, "true")
		b.click(`#diff-btn-split`)
		b.waitFor("page still responds", `String(!document.querySelector('#diff-content-split').classList.contains('hidden'))`, "true")
		if got := b.evaluate(`JSON.stringify(window.legacyErrors)`); got != "[]" {
			t.Fatalf("legacy browser errors: %s", got)
		}
	})
}

func TestBrowserFunctional_ChangesSplitActionInitialLoadingDisabled(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := worktreeActionsFixture(t, "", nil)
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/", "changes-initial-loading", func(b *composerFocusCDP) {
		b.waitFor("initial ready", `String(document.querySelector('[data-changes-primary]')?.getAttribute('aria-disabled')==='false')`, "true")
		b.evaluate(`(()=>{const original=window.fetch;window.fetch=async function(url,options){const response=await original(url,options);if(String(url).endsWith('/action-settings')&&!options?.method)await new Promise(resolve=>window.releaseSettings=resolve);return response;};window.oldSplit=document.querySelector('[data-changes-split]');htmx.ajax('GET','/tasks/layer-task/changes',{target:'#changes-content',swap:'innerHTML'});return 'ok';})()`)
		b.waitFor("both halves disabled during loading", `(()=>{const el=document.querySelector('[data-changes-primary]');return String(document.querySelector('[data-changes-split]')!==window.oldSplit&&typeof window.releaseSettings==='function'&&el.textContent==='Loading…'&&el.disabled&&document.querySelector('[data-changes-settings-toggle]').disabled&&el.getAttribute('aria-disabled')==='true'&&el.getAttribute('aria-busy')==='true'&&getComputedStyle(el).backgroundColor===getComputedStyle(document.querySelector('[data-changes-settings-toggle]')).backgroundColor);})()`, "true")
		b.evaluate(`document.querySelector('[data-changes-primary]').click();fetch('/fixture-state').then(r=>r.json()).then(s=>window.initialActions=s.actions.length);'ok'`)
		b.waitFor("loading click blocked", `String(window.initialActions===0)`, "true")
		b.evaluate(`window.releaseSettings();'ok'`)
		b.waitFor("action ready", `String(!document.querySelector('[data-changes-settings-toggle]').disabled&&document.querySelector('[data-changes-primary]').getAttribute('aria-disabled')==='false'&&document.querySelector('[data-changes-primary]').getAttribute('aria-busy')==='false')`, "true")
	})
}

func TestBrowserFunctional_ChangesSplitActionLongTargetLabel(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := worktreeActionsFixture(t, "", nil)
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/", "changes-long-target", func(b *composerFocusCDP) {
		b.waitFor("settings ready", `String(document.querySelector('[data-changes-primary]')?.getAttribute('aria-disabled')==='false')`, "true")
		b.evaluate(`(()=>{window.longTarget='recovery/task-04114335-pre-goal-verification-20260730193258';const original=window.fetch;window.fetch=async function(url,options){const response=await original(url,options);if(String(url).endsWith('/action-settings')&&(!options?.method||options.method==='GET')){const data=await response.json();data.local_branches.push(window.longTarget);data.preferences={...data.preferences,mode:'local',method:'ff',local_branch:window.longTarget};return new Response(JSON.stringify(data),{status:200});}return response;};window.oldSplit=document.querySelector('[data-changes-split]');htmx.ajax('GET','/tasks/layer-task/changes',{target:'#changes-content',swap:'innerHTML'});return 'ok';})()`)
		b.waitFor("compact label with full destination", `(()=>{const el=document.querySelector('[data-changes-primary]');return String(document.querySelector('[data-changes-split]')!==window.oldSplit&&el?.textContent==='Fast-forward'&&(el.title||el.dataset.ovTooltip)==='Fast-forward '+window.longTarget&&el.getAttribute('aria-label')==='Fast-forward '+window.longTarget&&el.getAttribute('aria-disabled')==='false')==='true'?'true':JSON.stringify({html:el?.outerHTML,target:window.longTarget});})()`, "true")
		b.waitFor("page-colored divider", `(()=>{const divider=getComputedStyle(document.querySelector('[data-changes-settings-toggle]'),'::before');return String(divider.width==='1px'&&divider.backgroundColor===getComputedStyle(document.querySelector('[data-diff-toolbar]')).backgroundColor);})()`, "true")
		b.click(`[data-changes-settings-toggle]`)
		b.waitFor("full branch remains selectable", `String(document.querySelector('[data-changes-local-branch]').selectedOptions[0].textContent===window.longTarget)`, "true")
		b.clickMenuBackdrop(`[data-changes-settings-toggle]`)
		b.evaluate(`document.addEventListener('htmx:afterRequest',()=>fetch('/fixture-state').then(r=>r.json()).then(s=>window.longTargetActions=s.actions));'ok'`)
		b.click(`[data-changes-primary]`)
		b.waitFor("full target dispatched", `String(window.longTargetActions?.length===1&&window.longTargetActions[0].changes_target===window.longTarget&&window.longTargetActions[0].merge_type==='ff')`, "true")
	})
}

func TestBrowserFunctional_ChangesSplitActionRightEdge(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := worktreeActionsFixture(t, "", nil)
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/", "changes-right-edge", func(b *composerFocusCDP) {
		b.waitFor("ready", `String(document.querySelector('[data-changes-primary]')?.getAttribute('aria-disabled')==='false')`, "true")
		raw := b.evaluate(`(()=>{const el=document.querySelector('[data-changes-settings-toggle]');el.scrollIntoView({block:'center'});const r=el.getBoundingClientRect();return JSON.stringify({x:r.right-1,y:r.top+r.height/2});})()`)
		var point struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		}
		if err := json.Unmarshal([]byte(raw), &point); err != nil {
			t.Fatal(err)
		}
		for _, event := range []map[string]any{
			{"type": "mouseMoved", "x": point.X, "y": point.Y},
			{"type": "mousePressed", "x": point.X, "y": point.Y, "button": "left", "buttons": 1, "clickCount": 1},
			{"type": "mouseReleased", "x": point.X, "y": point.Y, "button": "left", "buttons": 0, "clickCount": 1},
		} {
			b.call("Input.dispatchMouseEvent", event, nil)
		}
		b.waitFor("right edge opens menu", `String(document.querySelector('#changes-split-settings').matches(':popover-open'))`, "true")
	})
}

func TestBrowserFunctional_ChangesSplitActionMergedLocalSettings(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := worktreeActionsFixtureForTask(t, "", nil, &models.Task{ID: "layer-task", WorktreeBranch: "task/layer", MergeStatus: models.MergeStatusMerged})
	defer server.Close()
	for _, mode := range []string{"local", "pr"} {
		t.Run(mode, func(t *testing.T) {
			response, err := http.Post(server.URL+"/tasks/layer-task/worktree/action-settings", "application/json", strings.NewReader(`{"mode":"`+mode+`","repository":"https://github.com/upstream/repo","branch":"main","local_branch":"main","method":"ff"}`))
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				t.Fatalf("save preferences: %s", response.Status)
			}
			runComposerFocusCDP(t, chrome, server.URL+"/", "changes-merged-"+mode, func(b *composerFocusCDP) {
				b.waitFor("settings loaded", `String(document.querySelector('[data-changes-repo]')?.options.length===2)`, "true")
				if mode == "local" {
					b.waitFor("completed merge disables both halves", `String(document.querySelector('[data-changes-primary]').textContent==='Fast-forward'&&document.querySelector('[data-changes-primary]').disabled&&document.querySelector('[data-changes-settings-toggle]').disabled)`, "true")
					b.evaluate(`document.querySelector('[data-changes-settings-toggle]').click();document.querySelector('[data-changes-primary]').click();fetch('/fixture-state').then(r=>r.json()).then(s=>window.mergedActions=s.actions.length);'ok'`)
					b.waitFor("menu stays closed", `String(document.querySelector('[data-changes-settings-toggle]').getAttribute('aria-expanded')==='false')`, "true")
					b.waitFor("no repeated merge", `String(window.mergedActions===0)`, "true")
				} else {
					b.waitFor("PR actions remain available", `String(!document.querySelector('[data-changes-primary]').disabled&&!document.querySelector('[data-changes-settings-toggle]').disabled)`, "true")
					b.click(`[data-changes-settings-toggle]`)
					b.waitFor("PR settings open", `String(document.querySelector('[data-changes-settings-toggle]').getAttribute('aria-expanded')==='true')`, "true")
				}
			})
		})
	}
}
