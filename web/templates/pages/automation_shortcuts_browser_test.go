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
				b.waitFor("automation page ready", `String(document.readyState === 'complete' && !!document.querySelector('[data-breadcrumb-selector-button]') && !!document.getElementById('editor'))`, "true")
				key := func(code string) {
					b.evaluate(`document.activeElement.dispatchEvent(new KeyboardEvent('keydown',{code:'` + code + `',key:'` + code + `',shiftKey:true,metaKey:/Mac|iPhone|iPad/.test(navigator.platform),ctrlKey:!/Mac|iPhone|iPad/.test(navigator.platform),bubbles:true,cancelable:true}));'sent'`)
				}

				plainKey := func(code string) {
					b.evaluate(`document.activeElement.dispatchEvent(new KeyboardEvent('keydown',{code:'` + code + `',key:'` + code + `',bubbles:true,cancelable:true}));'sent'`)
				}

				b.waitFor("remaining shortcut hints", `String((function(){var button=document.querySelector('[data-breadcrumb-selector-button]');return button.title.includes('Last visited (') && button.title.includes('Previous (Left arrow)') && button.title.includes('Next (Right arrow)') && button.getAttribute('aria-keyshortcuts').includes('ArrowLeft')})())`, "true")
				// Both directions belong to project tabs, even with a breadcrumb present.
				for _, code := range []string{"ArrowLeft", "ArrowRight"} {
					b.waitFor("breadcrumb leaves project shortcut unhandled", `String((function(){var e=new KeyboardEvent('keydown',{code:'`+code+`',key:'`+code+`',shiftKey:true,metaKey:/Mac|iPhone|iPad/.test(navigator.platform),ctrlKey:!/Mac|iPhone|iPad/.test(navigator.platform),bubbles:true,cancelable:true});document.body.dispatchEvent(e);return !e.defaultPrevented})())`, "true")
				}
				plainKey("ArrowRight")
				b.waitFor("visit B", `location.pathname`, "/automations/b"+suffix)
				plainKey("ArrowLeft")
				b.waitFor("return to A", `location.pathname`, "/automations/a"+suffix)
				b.evaluate(`document.getElementById('editor').focus();'ready'`)
				plainKey("ArrowRight")
				key("KeyL")
				b.waitFor("editor does not navigate", `location.pathname`, "/automations/a"+suffix)
				b.evaluate(`var menu=document.createElement('div');menu.setAttribute('data-automation-live-menu','');menu.innerHTML='<button id="menu-action">Action</button>';document.body.appendChild(menu);document.getElementById('menu-action').focus();'ready'`)
				key("KeyL")
				b.waitFor("menu does not navigate", `location.pathname`, "/automations/a"+suffix)
				b.evaluate(`menu.remove();document.querySelector('[data-breadcrumb-selector-button]').focus();'ready'`)
				if mode == "edit" {
					b.evaluate(`var builder=document.createElement('div');builder.setAttribute('data-automation-yaml-builder','');builder.hasUnsavedAutomationChanges=()=>true;document.body.appendChild(builder);window.confirmCalls=0;window.confirm=()=>{window.confirmCalls++;return false};'ready'`)
					plainKey("ArrowRight")
					b.waitFor("dirty edit asks before leaving", `String(window.confirmCalls)`, "1")
					b.waitFor("cancel preserves page", `location.pathname`, "/automations/a"+suffix)
					b.evaluate(`window.confirm=()=>true;'ready'`)
				}
				key("KeyL")
				b.waitFor("last visited B", `location.pathname`, "/automations/b"+suffix)
				key("KeyL")
				b.waitFor("last visited toggles to A", `location.pathname`, "/automations/a"+suffix)
				if lists.Load() != 1 {
					t.Fatal("breadcrumb cycling should fetch its list once")
				}

			})
		})
	}
}
