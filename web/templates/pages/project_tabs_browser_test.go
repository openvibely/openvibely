package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_ProjectTabsAndBrowserScope(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	for _, desktop := range []bool{true, false} {
		t.Run(fmt.Sprintf("desktop=%v", desktop), func(t *testing.T) {
			var mu sync.Mutex
			selected := "p00"
			pins := []string{}
			projects := []models.Project{}
			for i := 0; i < 24; i++ {
				id := fmt.Sprintf("p%02d", i)
				projects = append(projects, models.Project{ID: id, Name: "Project " + id})
				pins = append(pins, id)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if static.ServeAsset(w, r) {
					return
				}
				if r.URL.Path == "/wails/runtime.js" {
					w.Header().Set("Content-Type", "text/javascript")
					_, _ = w.Write([]byte(`export const Window = {};`))
					return
				}
				if r.URL.Path == "/ui/preferences" {
					var req struct {
						ProjectID string    `json:"project_id"`
						Pins      *[]string `json:"pinned_project_ids"`
					}
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						http.Error(w, err.Error(), 400)
						return
					}
					mu.Lock()
					if req.ProjectID != "" {
						selected = req.ProjectID
					}
					if req.Pins != nil {
						pins = append([]string{}, (*req.Pins)...)
					}
					mu.Unlock()
					w.WriteHeader(204)
					return
				}
				if r.URL.Path != "/tasks" && r.URL.Path != "/chat" && r.URL.Path != "/schedule" {
					w.WriteHeader(204)
					return
				}
				mu.Lock()
				id := r.URL.Query().Get("project_id")
				if id == "" {
					id = selected
				}
				saved := append([]string{}, pins...)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/html")
				if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true" {
					fmt.Fprintf(w, `<div id="fixture-project">%s</div><a id="filtered-page" href="/tasks?project_id=%s&amp;view=board#keep" hx-get="/tasks?project_id=%s&amp;view=board" hx-push-url="/tasks?project_id=%s&amp;view=board#keep" hx-target="#main-content">Filtered tasks</a>`, id, id, id, id)
					return
				}
				ctx := layout.WithUIPreferences(layout.WithDesktopMode(context.Background(), desktop), layout.UIPreferences{PinnedProjectIDs: saved})
				var buf bytes.Buffer
				if err := layout.Base("Tasks", projects, id).Render(ctx, &buf); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				_, _ = w.Write(buf.Bytes())
			}))
			defer server.Close()
			runComposerFocusCDP(t, chrome, server.URL+"/tasks?view=board#keep", "project-tabs", func(browser *composerFocusCDP) {
				browser.waitFor("initial scope", `new URL(location.href).searchParams.get('project_id') || ''`, "p00")
				if got := browser.evaluate(`String(location.hash === '#keep' && new URL(location.href).searchParams.get('view') === 'board')`); got != "true" {
					t.Fatal("initial URL lost route state", got)
				}
				if !desktop {
					browser.waitFor("web selector", `String(!!document.querySelector('#sidebar #project-selector') && !document.getElementById('desktop-project-titlebar'))`, "true")
					// A second actual browser tab opens a different explicit project. Its
					// selection preference cannot alter this tab's canonical URL on reload.
					var target struct {
						ID string `json:"targetId"`
					}
					browser.call("Target.createTarget", map[string]any{"url": server.URL + "/tasks?project_id=p01"}, &target)
					mu.Lock()
					selected = "p01"
					mu.Unlock()
					browser.call("Page.reload", map[string]any{}, nil)
					browser.waitFor("independent web tab", `document.readyState === 'complete' ? document.getElementById('project-selector').value : ''`, "p00")
					browser.call("Target.closeTarget", map[string]any{"targetId": target.ID}, nil)
					return
				}
				browser.waitFor("tabs controller", `String(typeof window.openVibelyProjectTabsSync === 'function')`, "true")
				if got := browser.evaluate(`(function(){var bar=document.getElementById('desktop-project-titlebar').getBoundingClientRect(),tab=document.querySelector('.desktop-project-tab').getBoundingClientRect(),controls=document.querySelector('.desktop-window-controls');return String(tab.width>=240 && tab.top-bar.top>=5 && controls && Array.from(controls.querySelectorAll('button')).every(function(button){var r=button.getBoundingClientRect();return Math.abs((r.top+r.bottom-bar.top-bar.bottom)/2)<1 && getComputedStyle(button).getPropertyValue('--wails-draggable').trim()==='no-drag';}));})()`); got != "true" {
					t.Fatal("wide inset tabs and vertically centered window controls must share the titlebar", got)
				}
				if got := browser.evaluate(`(function(){var bar=document.getElementById('desktop-project-titlebar').getBoundingClientRect();var trigger=document.getElementById('project-selector-trigger').getBoundingClientRect();return String(['project-selector-trigger','project-settings-btn','new-project-btn'].every(function(id){var r=document.getElementById(id).getBoundingClientRect();return r.top>=bar.top && r.bottom<=bar.bottom && Math.abs((r.top+r.bottom-trigger.top-trigger.bottom)/2)<2;}));})()`); got != "true" {
					t.Fatal("titlebar selector and actions must share a centered row inside the bar", got)
				}
				if got := browser.evaluate(`String(document.getElementById('desktop-project-tabs').scrollWidth > document.getElementById('desktop-project-tabs').clientWidth && document.documentElement.scrollWidth <= innerWidth)`); got != "true" {
					t.Fatal("tabs must overflow within their own scrollport", got)
				}
				if got := browser.evaluate(`JSON.stringify([getComputedStyle(document.querySelector('.desktop-titlebar-space')).getPropertyValue('--wails-draggable').trim(),getComputedStyle(document.querySelector('[data-project-tab]')).getPropertyValue('--wails-draggable').trim(),getComputedStyle(document.getElementById('project-selector-trigger')).getPropertyValue('--wails-draggable').trim()])`); got != `["drag","no-drag","no-drag"]` {
					t.Fatal("drag regions", got)
				}
				if got := browser.evaluate(`(function(){var active=document.querySelector('[data-project-tab="p00"]'),inactive=document.querySelector('[data-project-tab="p01"]');return String(getComputedStyle(active.parentElement).backgroundColor!==getComputedStyle(inactive.parentElement).backgroundColor && getComputedStyle(active).backgroundColor==='rgba(0, 0, 0, 0)' && active.getBoundingClientRect().width===active.parentElement.getBoundingClientRect().width);})()`); got != "true" {
					t.Fatal("active background must cover the whole tab, not only the label", got)
				}
				// A fullscreen-sized content viewport must retain the same visible bar.
				// Native macOS fullscreen transitions require separate Wails verification.
				browser.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1920, "height": 1080, "deviceScaleFactor": 1, "mobile": false}, nil)
				if got := browser.evaluate(`(function(){var tab=document.querySelector('.desktop-project-tab').getBoundingClientRect();return String(tab.top>=5 && tab.bottom<=46);})()`); got != "true" {
					t.Fatal("fullscreen-sized viewport lost window controls or tabs", got)
				}
				browser.call("Emulation.clearDeviceMetricsOverride", map[string]any{}, nil)
				browser.click(`[data-nav-base="/schedule"]`)
				browser.waitFor("project A schedule", `location.pathname`, "/schedule")
				browser.click(`[data-project-tab="p01"]`)
				browser.waitFor("new project starts independently", `location.pathname+location.search`, "/chat?project_id=p01")
				browser.click(`[data-nav-base="/tasks"]`)
				browser.waitFor("project B tasks", `location.pathname`, "/tasks")
				browser.click(`[data-project-tab="p00"]`)
				browser.waitFor("project A remembers schedule", `location.pathname+location.search`, "/schedule?project_id=p00")
				browser.click(`[data-project-tab="p01"]`)
				browser.waitFor("project B remembers tasks", `location.pathname+location.search`, "/tasks?project_id=p01")
				browser.click("#filtered-page")
				browser.waitFor("project B filters", `location.search+location.hash`, "?project_id=p01&view=board#keep")
				browser.click(`[data-project-tab="p00"]`)
				browser.waitFor("project A unaffected by B filters", `location.pathname+location.search`, "/schedule?project_id=p00")
				browser.evaluate(`String(window.beforeProjectTabsReload = true)`)
				browser.call("Page.reload", map[string]any{}, nil)
				browser.waitFor("navigation session reload", `String(!window.beforeProjectTabsReload && typeof window.openVibelyProjectTabsSync === 'function')`, "true")
				browser.click(`[data-project-tab="p01"]`)
				browser.waitFor("project B route survives reload", `location.pathname+location.search+location.hash`, "/tasks?project_id=p01&view=board#keep")
				browser.click(`[data-nav-base="/schedule"]`)
				browser.waitFor("leave filtered tasks", `location.pathname`, "/schedule")
				browser.click(`[data-nav-base="/tasks"]`)
				browser.waitFor("return to unfiltered tasks", `location.pathname+location.search`, "/tasks?project_id=p01")
				browser.click(`[data-project-tab="p01"]`)
				browser.waitFor("shared navigation", `location.search`, "?project_id=p01")
				browser.waitFor("active tab", `document.querySelector('[data-project-tab="p01"]').getAttribute('aria-selected')`, "true")
				browser.waitFor("loaded project", `document.getElementById('fixture-project')?.textContent || ''`, "p01")
				browser.click(`[data-project-tab="p01"]`)
				key := func(key, code string) {
					params := map[string]any{"type": "keyDown", "key": key, "code": code}
					if key == "Enter" {
						params["text"] = "\r"
						params["unmodifiedText"] = "\r"
						params["windowsVirtualKeyCode"] = 13
					}
					browser.call("Input.dispatchKeyEvent", params, nil)
					browser.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": key, "code": code}, nil)
				}
				key("ArrowRight", "ArrowRight")
				browser.waitFor("keyboard roving focus", `document.activeElement.dataset.projectTab || ''`, "p02")
				if got := browser.evaluate(`String(document.activeElement.matches(':focus-visible'))`); got != "true" {
					t.Fatal("keyboard focus not visible")
				}
				key("Enter", "Enter")
				browser.waitFor("keyboard switch", `location.search`, "?project_id=p02")
				browser.click("#project-pin-toggle")
				browser.waitFor("unpin keeps project", `String(!document.querySelector('[data-project-tab="p02"]') && document.getElementById('project-selector').value === 'p02')`, "true")
				browser.waitFor("pin save", `String(document.getElementById('project-pin-toggle').getAttribute('aria-pressed'))`, "false")
				browser.waitFor("serialized saves", `String(document.getElementById('desktop-project-titlebar').dataset.pinnedProjects.includes('p02'))`, "false")
				// Wait for the server-observed preference before simulating a relaunch.
				browser.waitFor("save completed", `String(document.readyState)`, "complete")
				// A marker request is queued after the UI's fetch on the browser event loop.
				browser.evaluateAwait(`new Promise(resolve => { function check() { fetch('/tasks').then(r=>r.text()).then(html=> { if (!html.includes('data-project-tab="p02"')) resolve('saved'); else setTimeout(check,20); }); } check(); })`)
				browser.evaluate(`String(window.beforeProjectTabsReload = true)`)
				browser.call("Page.reload", map[string]any{}, nil)
				browser.waitFor("restored unpinned project", `!window.beforeProjectTabsReload && document.readyState === 'complete' ? String(!document.querySelector('[data-project-tab="p02"]') && document.getElementById('project-selector').value === 'p02') : ''`, "true")
				browser.click("#project-pin-toggle")
				browser.waitFor("repin", `String(!!document.querySelector('[data-project-tab="p02"]'))`, "true")
				browser.click(`[data-close-project="p02"]`)
				browser.waitFor("closing active tab chooses neighbor", `location.search`, "?project_id=p23")
				browser.waitFor("closed tab", `String(!document.querySelector('[data-project-tab="p02"]'))`, "true")
				key("Home", "Home")
				browser.click("#project-selector-trigger")
				browser.waitFor("full selector", `String(document.getElementById('project-selector-dialog').open)`, "true")
				browser.click("#project-selector-search")
				browser.typeText("Project p03")
				browser.click(`[data-project-selector-option][data-project-id="p03"]`)
				browser.waitFor("selector and tabs share state", `String(location.search === '?project_id=p03' && document.querySelector('[data-project-tab="p03"]').getAttribute('aria-selected') === 'true')`, "true")
				// History restoration updates both tab selection and sidebar URLs.
				browser.navigateHistory(-1)
				browser.waitFor("history project", `document.getElementById('project-selector').value`, "p23")
				browser.waitFor("history sidebar scope", `String(Array.from(document.querySelectorAll('[data-nav-base]')).every(a=>a.getAttribute('href').includes('project_id=p23')))`, "true")
			})
			mu.Lock()
			final := strings.Join(pins, ",")
			mu.Unlock()
			if desktop && strings.Contains(final, "p02") {
				t.Fatal("closed pin persisted", final)
			}
		})
	}
}
