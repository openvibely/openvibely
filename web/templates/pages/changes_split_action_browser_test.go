package pages

import (
	"encoding/json"
	"testing"
)

func TestBrowserFunctional_ChangesSplitActionPreferencesAndOneClick(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := worktreeActionsFixture(t, "", nil)
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/", "changes-split-action", func(b *composerFocusCDP) {
		b.waitFor("upstream default ready", `String(document.querySelector('[data-changes-primary]')?.textContent==='Create PR to upstream' && !document.querySelector('[data-changes-primary]').matches(':disabled,[aria-disabled="true"]'))`, "true")
		b.evaluate(`document.addEventListener('htmx:afterRequest',()=>fetch('/fixture-state').then(r=>r.json()).then(s=>window.actionState=s));'ok'`)
		b.click(`[data-changes-primary]`)
		b.waitFor("PR submitted once", `String(window.actionState?.actions.length===1&&window.actionState.actions[0].base==='main'&&window.actionState.actions[0].target_repo==='https://github.com/upstream/repo')`, "true")
		b.click(`[data-changes-settings-toggle]`)
		b.waitFor("settings open", `String(document.querySelector('#changes-split-settings').matches(':popover-open'))`, "true")
		b.click(`[data-changes-mode="local"]`)
		b.waitFor("local default saved", `String(document.querySelector('[data-changes-primary]').textContent==='Merge into main'&&!document.querySelector('[data-changes-primary]').matches(':disabled,[aria-disabled="true"]'))`, "true")
		b.evaluate(`(()=>{const branch=document.querySelector('[data-changes-local-branch]');branch.value='develop';branch.dispatchEvent(new Event('change',{bubbles:true}));const method=document.querySelector('[data-changes-method]');method.value='squash';method.dispatchEvent(new Event('change',{bubbles:true}));return 'ok';})()`)
		b.waitFor("squash preference saved", `String(document.querySelector('[data-changes-primary]').textContent==='Squash into develop'&&!document.querySelector('[data-changes-primary]').matches(':disabled,[aria-disabled="true"]'))`, "true")
		b.clickMenuBackdrop(`[data-changes-settings-toggle]`)
		b.click(`[data-changes-primary]`)
		b.waitFor("local action dispatched once", `String(window.actionState?.actions.length===2&&window.actionState.actions[1].merge_type==='squash'&&window.actionState.actions[1].changes_target==='develop')`, "true")
		b.evaluate(`window.oldSplit=document.querySelector('[data-changes-split]');htmx.ajax('GET','/tasks/layer-task/changes',{target:'#changes-content',swap:'innerHTML'});'ok'`)
		b.waitFor("saved action restored after refresh", `String(document.querySelector('[data-changes-split]')!==window.oldSplit&&document.querySelector('[data-changes-primary]').textContent==='Squash into develop'&&!document.querySelector('[data-changes-primary]').matches(':disabled,[aria-disabled="true"]'))`, "true")
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
			b.waitFor("whole split shrinks", `(()=>{const root=document.querySelector('.ov-changes-split');return String(getComputedStyle(root).transform!=='none'&&Array.from(root.querySelectorAll('.btn')).every(el=>getComputedStyle(el).transform==='none'));})()`, "true")
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
		b.waitFor("fork action enabled despite warning", `String(!document.querySelector('[data-changes-primary]').matches(':disabled,[aria-disabled="true"]')&&document.querySelector('[data-changes-primary]').textContent==='Create PR to me/repo'&&!document.querySelector('[data-changes-error]').hidden)`, "true")
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
		b.waitFor("rebase enabled", `String(document.querySelector('[data-changes-primary]').textContent==='Rebase onto develop'&&!document.querySelector('[data-changes-primary]').matches(':disabled,[aria-disabled="true"]'))`, "true")
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
		b.waitFor("missing target disabled action", `String(document.querySelector('[data-changes-split]')!==window.oldSplit&&document.querySelector('[data-changes-primary]').textContent==='Merge into deleted'&&document.querySelector('[data-changes-primary]').matches(':disabled,[aria-disabled="true"]'))`, "true")
		b.click(`[data-changes-settings-toggle]`)
		b.waitFor("local controls accessible", `String(!document.querySelector('[data-changes-mode="local"]').disabled&&!document.querySelector('[data-changes-local-fields]').hidden)`, "true")
		b.evaluate(`(()=>{const el=document.querySelector('[data-changes-local-branch]');el.value='main';el.dispatchEvent(new Event('change'));return 'ok';})()`)
		b.waitFor("replacement eligible", `String(document.querySelector('[data-changes-primary]').textContent==='Merge into main'&&!document.querySelector('[data-changes-primary]').matches(':disabled,[aria-disabled="true"]'))`, "true")
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
