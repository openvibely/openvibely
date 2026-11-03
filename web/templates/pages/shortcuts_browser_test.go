package pages

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
)

func TestBrowserFunctional_Shortcuts(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path != "/shortcuts" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := Shortcuts([]models.Project{{ID: "p", Name: "Project"}}, "p").Render(r.Context(), w); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/shortcuts?project_id=p", "shortcuts", func(b *composerFocusCDP) {
		for _, platform := range []string{"MacIntel", "Win32"} {
			// Reload to exercise initial OS detection, not an artificial display selector.
			b.call("Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": `Object.defineProperty(navigator,'platform',{configurable:true,get:()=> '` + platform + `'});`}, nil)
			b.reload()
			b.waitFor("platform ready", `String(navigator.platform)`, platform)
			b.waitFor("page initialized", `String(document.querySelector('#shortcuts-container')?._shortcutsInitialized)`, "true")
			want := "Ctrl"
			macOnly := "true"
			if platform == "MacIntel" {
				want = "⌘"
				macOnly = "false"
			}
			b.waitFor("automatic modifier label", `document.querySelector('[data-shortcut-key="mod"]').textContent`, want)
			b.waitFor("mac-only visibility", `String(document.querySelector('[data-mac-only="true"]').hidden)`, macOnly)
			b.waitFor("search width", `String(document.querySelector('[data-shortcut-search]').getBoundingClientRect().width<=320)`, "true")
			b.evaluate(`var search=document.querySelector('[data-shortcut-search]');search.value='steer';search.dispatchEvent(new Event('input'));'filtered'`)
			b.waitFor("search result", `Array.from(document.querySelectorAll('[data-shortcut-row]:not([hidden]) [data-shortcut-action]')).map(el=>el.textContent).join('|')`, "Steer the current turn")
			b.evaluate(`var category=document.querySelector('[data-shortcut-category]');category.value='Automations';category.dispatchEvent(new Event('change'));'category'`)
			b.waitFor("combined filters empty", `String(!document.querySelector('[data-shortcut-empty]').hidden)`, "true")
			b.evaluate(`document.querySelector('[data-shortcut-reset]').click();'reset'`)
			b.waitFor("reset restores list and focus", `String(document.querySelector('[data-shortcut-empty]').hidden && document.activeElement===document.querySelector('[data-shortcut-search]') && document.querySelector('[data-shortcut-category]').value==='')`, "true")
			b.evaluate(`category.value='Automations';category.dispatchEvent(new Event('change'));'category'`)
			b.waitFor("category only", `Array.from(document.querySelectorAll('[data-shortcut-group]:not([hidden])')).map(el=>el.dataset.shortcutGroup).join('|')`, "Automations")
		}
	})
}
