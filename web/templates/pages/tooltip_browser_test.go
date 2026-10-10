package pages

import (
	"fmt"
	templateui "github.com/openvibely/openvibely/web/templates"
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
		b.evaluate(`document.getElementById('native').focus();document.getElementById('native').dispatchEvent(new FocusEvent('focusin',{bubbles:true}));'ready'`)

		b.waitFor("shortcut badge", `document.querySelector('#ov-shared-tooltip kbd').textContent`, "⌘⇧D")
		b.waitFor("native title suppressed", `String(!document.getElementById('native').hasAttribute('title'))`, "true")
		b.evaluate(`document.getElementById('native').title='Loading models';'updated'`)
		b.waitFor("dynamic title", `document.getElementById('ov-shared-tooltip').textContent`, "Loading models")
		b.evaluate(`document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));'closed'`)
		b.waitFor("description preserved", `document.getElementById('native').getAttribute('aria-describedby')`, "existing")
		b.waitFor("no native tooltip after Escape", `String(!document.getElementById('native').hasAttribute('title'))`, "true")
		b.evaluate(`var fresh=document.createElement('button');fresh.id='fresh';fresh.title='Review changes (Ctrl+Shift+D)';fresh.textContent='Review';document.body.appendChild(fresh);'inserted'`)
		b.waitFor("inserted tooltip adopted", `document.getElementById('fresh').dataset.ovTooltip`, "Review changes (Ctrl+Shift+D)")
		b.evaluate(`document.getElementById('fresh').dispatchEvent(new PointerEvent('pointerover',{bubbles:true}));'hovered'`)
		b.waitFor("Windows shortcut badge", `document.querySelector('#ov-shared-tooltip kbd').textContent`, "Ctrl+Shift+D")
		b.evaluate(`document.body.style.setProperty('--ov-menu-surface','#ffffff');'theme'`)
		b.waitFor("theme follows menu surface", `getComputedStyle(document.getElementById('ov-shared-tooltip')).backgroundColor`, "rgb(255, 255, 255)")
		b.evaluate(`document.getElementById('fresh').remove();'removed'`)
		b.waitFor("removed owner dismisses tooltip", `String(document.getElementById('ov-shared-tooltip').hidden)`, "true")
		b.waitFor("latest label retained", `document.getElementById('native').dataset.ovTooltip`, "Loading models")
		b.evaluate(`document.getElementById('state').focus();document.getElementById('state').dispatchEvent(new FocusEvent('focusin',{bubbles:true}));'ready'`)
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
	})
}
