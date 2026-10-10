package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/components"
)

func TestBrowserFunctional_AutomationBreadcrumbShortcuts(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	for _, mode := range []string{"live", "edit"} {
		t.Run(mode, func(t *testing.T) {
			var lists atomic.Int32
			suffix := ""
			if mode == "edit" {
				suffix = "/builder"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/breadcrumb-selectors/automations" {
					lists.Add(1)
					items := []models.BreadcrumbSelectorItem{}
					for _, id := range []string{"a", "b", "c"} {
						items = append(items, models.BreadcrumbSelectorItem{ID: id, Name: id, URL: "/automations/" + id + suffix + "?project_id=p"})
					}
					_ = components.BreadcrumbSelectorResults("Automation", r.URL.Query().Get("current_id"), items, false, false).Render(r.Context(), w)
					return
				}
				if !strings.HasPrefix(r.URL.Path, "/automations/") {
					http.NotFound(w, r)
					return
				}
				id := strings.Split(r.URL.Path, "/")[2]
				fmt.Fprint(w, `<!doctype html><html><body><script>window.openVibelyNavigate=async function(url){var html=await (await fetch(url)).text();var doc=new DOMParser().parseFromString(html,'text/html');document.querySelector('nav').replaceWith(doc.querySelector('nav'));history.pushState({},'',url);document.dispatchEvent(new CustomEvent('htmx:afterSwap'));};</script>`)
				var c templ.Component = automationBreadcrumb("p", id, id, "", false)
				if mode == "edit" {
					c = automationEditableBreadcrumb("p", id, id)
				}
				_ = c.Render(r.Context(), w)
				fmt.Fprint(w, `<textarea id="editor"></textarea></body></html>`)
			}))
			defer server.Close()
			runComposerFocusCDP(t, chrome, server.URL+"/automations/a"+suffix+"?project_id=p", "automation-shortcuts-"+mode, func(b *composerFocusCDP) {
				key := func(code string) {
					b.evaluate(`document.activeElement.dispatchEvent(new KeyboardEvent('keydown',{code:'` + code + `',key:'` + code + `',shiftKey:true,metaKey:/Mac|iPhone|iPad/.test(navigator.platform),ctrlKey:!/Mac|iPhone|iPad/.test(navigator.platform),bubbles:true,cancelable:true}));'sent'`)
				}
				b.waitFor("platform shortcut hints", `String((function(){var button=document.querySelector('[data-breadcrumb-selector-button]'),mac=/Mac|iPhone|iPad/.test(navigator.platform);return button.title.includes(mac?'⌘⇧↑/↓':'Ctrl+Shift+↑/↓') && button.title.includes(mac?'⌘⇧L':'Ctrl+Shift+L') && button.getAttribute('aria-keyshortcuts').includes(mac?'Meta+Shift+L':'Control+Shift+L')})())`, "true")
				b.waitFor("tooltip", `String(document.querySelector('[data-breadcrumb-selector-button]').title.includes('previous/next:') && document.querySelector('[data-breadcrumb-selector-button]').title.includes('last visited:'))`, "true")
				b.evaluate(`document.getElementById('editor').focus();'ready'`)
				key("ArrowDown")
				b.waitFor("editor does not navigate", `location.pathname`, "/automations/a"+suffix)
				if lists.Load() != 0 {
					t.Fatal("editor shortcut fetched navigation list")
				}
				b.evaluate(`document.querySelector('[data-breadcrumb-selector-button]').focus();'ready'`)
				for _, step := range []struct{ code, id string }{{"ArrowDown", "b"}, {"ArrowDown", "c"}, {"ArrowUp", "b"}, {"KeyL", "c"}, {"KeyL", "b"}, {"ArrowUp", "a"}, {"ArrowUp", "a"}} {
					key(step.code)
					b.waitFor("automation "+step.id, `location.pathname`, "/automations/"+step.id+suffix)
				}
				if lists.Load() != 1 {
					t.Fatalf("cycling fetched list %d times", lists.Load())
				}
			})
		})
	}
}
