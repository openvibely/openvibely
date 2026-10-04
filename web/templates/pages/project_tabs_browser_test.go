package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

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
			locations := "{}"
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
				if strings.HasPrefix(r.URL.Path, "/projects/") && strings.HasSuffix(r.URL.Path, "/edit") {
					w.Header().Set("Content-Type", "text/html")
					fmt.Fprintf(w, `<div id="settings-project">%s</div>`, r.URL.Path)
					return
				}
				if r.URL.Path == "/projects/new" {
					w.Header().Set("Content-Type", "text/html")
					fmt.Fprint(w, `<div id="create-project-fixture">Create New Project</div>`)
					return
				}
				if r.URL.Path == "/ui/preferences" {
					var req struct {
						Locations map[string]string `json:"project_locations"`
						ProjectID string            `json:"project_id"`
						Pins      *[]string         `json:"pinned_project_ids"`
					}
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						http.Error(w, err.Error(), 400)
						return
					}
					mu.Lock()
					if req.Locations != nil {
						data, _ := json.Marshal(req.Locations)
						locations = string(data)
					}
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
				savedLocations := locations
				mu.Unlock()
				w.Header().Set("Content-Type", "text/html")
				if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true" {
					fmt.Fprintf(w, `<div id="fixture-project">%s</div><a id="filtered-page" href="/tasks?project_id=%s&amp;view=board#keep" hx-get="/tasks?project_id=%s&amp;view=board" hx-push-url="/tasks?project_id=%s&amp;view=board#keep" hx-target="#main-content">Filtered tasks</a>`, id, id, id, id)
					return
				}
				ctx := layout.WithUIPreferences(layout.WithDesktopMode(context.Background(), desktop), layout.UIPreferences{PinnedProjectIDs: saved, ProjectLocations: savedLocations})
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
				if got := browser.evaluate(`String(document.querySelector('.drawer-content').getBoundingClientRect().bottom <= innerHeight + 1)`); got != "true" {
					t.Fatal("shared page shell extends below viewport", got)
				}
				browser.click("#project-selector-trigger")
				browser.waitFor("menu ready for hover", `String(document.getElementById('project-selector-dialog').open)`, "true")
				activeColor := browser.evaluate(`getComputedStyle(document.querySelector('#project-selector-dialog [data-selector-active]')).backgroundColor`)
				for _, selector := range []string{"#new-project-btn", "[data-project-selector-option]", "#new-project-btn"} {
					var hoverPoint struct{ X, Y float64 }
					if err := json.Unmarshal([]byte(browser.evaluate(`JSON.stringify((function(){var r=document.querySelector('`+selector+`').getBoundingClientRect();return {X:r.x+r.width/2,Y:r.y+r.height/2};})())`)), &hoverPoint); err != nil {
						t.Fatal(err)
					}
					browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": hoverPoint.X, "y": hoverPoint.Y}, nil)
					browser.waitFor("one menu highlight", `String(document.querySelectorAll('#project-selector-dialog [data-selector-active]').length===1 && document.querySelector('`+selector+`').hasAttribute('data-selector-active'))`, "true")
					browser.waitFor("standard menu highlight color", `getComputedStyle(document.querySelector('`+selector+`')).backgroundColor`, activeColor)
				}

				browser.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Escape"}, nil)
				browser.waitFor("hover test menu closed", `String(document.getElementById('project-selector-dialog').open)`, "false")
				if !desktop {
					browser.waitFor("web selector", `String(!!document.querySelector('#desktop-project-titlebar #project-selector') && !document.querySelector('[data-wml-window]'))`, "true")
					browser.waitFor("web plus idle background", `getComputedStyle(document.getElementById('project-selector-trigger')).backgroundColor`, "rgba(0, 0, 0, 0)")
					browser.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 390, "height": 844, "deviceScaleFactor": 1, "mobile": true}, nil)
					browser.waitFor("mobile keeps sidebar selector", `String(!!document.querySelector('#sidebar #project-selector') && getComputedStyle(document.getElementById('desktop-project-titlebar')).display === 'none')`, "true")
					browser.call("Emulation.clearDeviceMetricsOverride", map[string]any{}, nil)
					browser.waitFor("wide selector restored", `String(!!document.querySelector('#desktop-project-titlebar #project-selector'))`, "true")
					// A second actual browser tab opens a different explicit project. Its
					// selection preference cannot alter this tab's canonical URL on reload.
					var target struct {
						ID string `json:"targetId"`
					}
					browser.call("Target.createTarget", map[string]any{"url": server.URL + "/tasks?project_id=p01"}, &target)
					mu.Lock()
					selected = "p01"
					mu.Unlock()
					browser.reload()
					browser.waitFor("independent web tab", `document.readyState === 'complete' ? document.getElementById('project-selector').value : ''`, "p00")
					browser.call("Target.closeTarget", map[string]any{"targetId": target.ID}, nil)
					return
				}
				browser.waitFor("tabs controller", `String(typeof window.openVibelyProjectTabsSync === 'function')`, "true")
				if got := browser.evaluate(`String(document.querySelectorAll('#new-project-btn').length === 1 && document.getElementById('desktop-project-tabs').lastElementChild.hasAttribute('data-project-selector') && document.getElementById('project-selector-trigger').querySelector('svg[aria-hidden="true"]') !== null && document.getElementById('project-selector-dialog').contains(document.getElementById('new-project-btn')) && !document.getElementById('project-pin-toggle'))`); got != "true" {
					t.Fatal("create must follow the last tab with no standalone pin control", got)
				}
				// Simulate WebView startup assigning focus before any keyboard input.
				if got := browser.evaluate(`(function(){var tab=document.querySelector('[data-project-tab]');tab.focus();return getComputedStyle(tab).outlineStyle;})()`); got != "none" {
					t.Fatal("startup tab focus must not show a selection-like outline", got)
				}
				browser.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "ArrowRight", "code": "ArrowRight"}, nil)
				browser.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "ArrowRight", "code": "ArrowRight"}, nil)
				if got := browser.evaluate(`getComputedStyle(document.activeElement).outlineStyle`); got != "solid" {
					t.Fatal("keyboard tab navigation must retain a focus outline", got)
				}
				// Selection happens on press, before release or drag movement.
				for _, id := range []string{"p01", "p00"} {
					var point struct{ X, Y float64 }
					if err := json.Unmarshal([]byte(browser.evaluate(`JSON.stringify((function(){var r=document.querySelector('[data-project-tab="`+id+`"] ').getBoundingClientRect();return {X:r.x+80,Y:r.y+15};})())`)), &point); err != nil {
						t.Fatal(err)
					}
					browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": point.X, "y": point.Y, "button": "left", "buttons": 1, "clickCount": 1}, nil)
					browser.waitFor("activate tab before mouse release", `document.querySelector('[data-project-tab][aria-selected="true"]').dataset.projectTab`, id)
					browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": point.X, "y": point.Y, "button": "left", "buttons": 0, "clickCount": 1}, nil)
					browser.waitFor("project navigation after press", `new URLSearchParams(location.search).get('project_id')`, id)
				}
				// Global shortcuts activate neighboring tabs and wrap in displayed order.
				for _, modifiers := range []int{12, 10} { // Meta+Shift and Control+Shift.
					for _, key := range []string{"ArrowLeft", "ArrowRight", "ArrowRight", "ArrowLeft"} {
						want := browser.evaluate(`(function(){var tabs=Array.from(document.querySelectorAll('[data-project-tab]'));var i=tabs.findIndex(t=>t.getAttribute('aria-selected')==='true');return tabs[(i+(` + fmt.Sprintf("%q", key) + `==='ArrowRight'?1:-1)+tabs.length)%tabs.length].dataset.projectTab;})()`)
						composerID := "message-input"
						if key == "ArrowLeft" {
							composerID = "task-message-input"
						}
						browser.evaluate(`(function(){var input=document.createElement('textarea');input.id=` + fmt.Sprintf("%q", composerID) + `;input.dataset.shortcutFixture='true';document.body.appendChild(input);input.focus();return 'focused';})()`)
						browser.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": key, "code": key, "modifiers": modifiers}, nil)
						browser.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": key, "code": key}, nil)
						browser.waitFor("shortcut project navigation", `new URLSearchParams(location.search).get('project_id')`, want)
						browser.evaluate(`document.querySelectorAll('[data-shortcut-fixture]').forEach(el=>el.remove()); "removed"`)
					}
					if got := browser.evaluate(`(function(){var input=document.createElement('textarea');document.body.appendChild(input);input.focus();var event=new KeyboardEvent('keydown',{key:'ArrowLeft',metaKey:` + fmt.Sprint(modifiers == 12) + `,ctrlKey:` + fmt.Sprint(modifiers == 10) + `,shiftKey:true,bubbles:true,cancelable:true});input.dispatchEvent(event);input.remove();return String(event.defaultPrevented);})()`); got != "false" {
						t.Fatal("project shortcut intercepted text selection", got)
					}
				}
				// Hovering the next tab hides separators, not the selected tab's curved foot.
				var hoverPoint struct{ X, Y float64 }
				if err := json.Unmarshal([]byte(browser.evaluate(`JSON.stringify((function(){var r=document.querySelector('[data-project-tab="p01"]').getBoundingClientRect();return {X:r.x+80,Y:r.y+15};})())`)), &hoverPoint); err != nil {
					t.Fatal(err)
				}
				browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": hoverPoint.X, "y": hoverPoint.Y}, nil)
				if got := browser.evaluate(`getComputedStyle(document.querySelector('[data-project-tab="p00"]').parentElement,'::after').opacity`); got != "1" {
					t.Fatal("active tab curve disappeared on adjacent hover", got)
				}
				tabHoverColor := browser.evaluate(`getComputedStyle(document.querySelector('[data-project-tab="p01"]')).backgroundColor`)
				if err := json.Unmarshal([]byte(browser.evaluate(`JSON.stringify((function(){var r=document.querySelector('[data-close-project="p01"]').getBoundingClientRect();return {X:r.x+r.width/2,Y:r.y+r.height/2};})())`)), &hoverPoint); err != nil {
					t.Fatal(err)
				}
				browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": hoverPoint.X, "y": hoverPoint.Y}, nil)
				if got := browser.evaluate(`getComputedStyle(document.querySelector('[data-project-tab="p01"]')).backgroundColor`); got != tabHoverColor {
					t.Fatal("inactive tab hover background disappeared over close button", got, tabHoverColor)
				}
				if got := browser.evaluate(`getComputedStyle(document.querySelector('[data-close-project="p01"]')).backgroundColor`); got == "rgba(0, 0, 0, 0)" || got == tabHoverColor {
					t.Fatal("close button needs its own hover highlight", got)
				}
				// Reorder with real mouse input, in both directions, without navigation.
				if got := browser.evaluate(`getComputedStyle(document.querySelector('[data-project-tab="p02"]').parentElement,'::after').width`); got != "2px" {
					t.Fatal("missing background tab separator", got)
				}
				dragTab := func(id string, delta float64) {
					browser.waitFor("tab motion settled", `String(document.querySelectorAll('.desktop-project-tab').length > 0 && Array.from(document.querySelectorAll('.desktop-project-tab')).every(t=>t.getAnimations().length===0))`, "true")
					var p struct{ X, Y float64 }
					if err := json.Unmarshal([]byte(browser.evaluate(`JSON.stringify((function(){var r=document.querySelector('[data-project-tab="`+id+`"] ').getBoundingClientRect();return {X:r.x+80,Y:r.y+15};})())`)), &p); err != nil {
						t.Fatal(err)
					}
					browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": p.X, "y": p.Y, "button": "left", "buttons": 1, "clickCount": 1}, nil)
					for i := 1; i <= 10; i++ {
						browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": p.X + delta*float64(i)/10, "y": p.Y, "buttons": 1}, nil)
					}
					if got := browser.evaluate(`String(Math.abs(document.querySelector('[data-project-tab="` + id + `"]').getBoundingClientRect().left + 80 - ` + fmt.Sprint(p.X+delta) + `) < 2)`); got != "true" {
						t.Fatal("dragged tab must follow pointer continuously", got)
					}
					browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": p.X + delta, "y": p.Y, "button": "left", "buttons": 0, "clickCount": 1}, nil)
				}
				browser.evaluate(`String(document.getElementById('desktop-project-tabs').scrollLeft = 0)`)
				dragTab("p00", 130)
				browser.waitFor("drag right", `Array.from(document.querySelectorAll('[data-project-tab]')).slice(0,2).map(t=>t.dataset.projectTab).join(',')`, "p01,p00")
				browser.waitFor("drag preserves active project", `document.getElementById('project-selector').value`, "p00")
				browser.waitFor("reordered pins saved", `JSON.parse(document.getElementById('desktop-project-titlebar').dataset.pinnedProjects).slice(0,2).join(',')`, "p01,p00")
				for deadline := time.Now().Add(3 * time.Second); ; {
					mu.Lock()
					savedOrder := strings.Join(pins[:2], ",")
					mu.Unlock()
					if savedOrder == "p01,p00" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("dragged order not persisted: %s", savedOrder)
					}
					time.Sleep(10 * time.Millisecond)
				}
				dragTab("p00", -130)
				browser.waitFor("drag left", `Array.from(document.querySelectorAll('[data-project-tab]')).slice(0,2).map(t=>t.dataset.projectTab).join(',')`, "p00,p01")
				// Drag the last tab into the + section without releasing the mouse.
				browser.evaluate(`document.getElementById('desktop-project-tabs').scrollLeft = 100000; 'ready'`)
				var wall struct{ X, Y float64 }
				if err := json.Unmarshal([]byte(browser.evaluate(`JSON.stringify((function(){var r=document.querySelector('[data-project-tab="p23"]').getBoundingClientRect();return {X:r.x+80,Y:r.y+15};})())`)), &wall); err != nil {
					t.Fatal(err)
				}
				browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": wall.X, "y": wall.Y, "button": "left", "buttons": 1, "clickCount": 1}, nil)
				browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 1200, "y": wall.Y, "buttons": 1}, nil)
				if got := browser.evaluate(`String(Math.abs(new DOMMatrix(getComputedStyle(document.querySelector('[data-project-tab="p23"]').parentElement).transform).m41) < 0.5)`); got != "true" {
					t.Fatal("last tab must stop at its resting position", got)
				}
				browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": 1200, "y": wall.Y, "button": "left", "buttons": 0, "clickCount": 1}, nil)
				browser.evaluate(`document.querySelector('[data-project-tab="p00"]').scrollIntoView({block:'nearest',inline:'start'}); 'ready'`)
				browser.waitFor("wall drag settled", `String(Array.from(document.querySelectorAll(".desktop-project-tab")).every(t=>t.getAnimations().length===0))`, "true")
				browser.click(`[data-project-tab="p00"]`)
				var point struct{ X, Y float64 }
				if err := json.Unmarshal([]byte(browser.evaluate(`JSON.stringify((function(){var r=document.querySelector('[data-project-tab="p00"]').getBoundingClientRect();return {X:r.x+40,Y:r.y+15};})())`)), &point); err != nil {
					t.Fatal(err)
				}
				browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": point.X, "y": point.Y, "button": "left", "buttons": 1, "clickCount": 1}, nil)
				browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 0, "y": point.Y, "buttons": 1}, nil)
				if got := browser.evaluate(`String(Math.abs(new DOMMatrix(getComputedStyle(document.querySelector('[data-project-tab="p00"]').parentElement).transform).m41) < 0.5)`); got != "true" {
					t.Fatal("first tab must stop at its resting position", got)
				}
				browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": 0, "y": point.Y, "button": "left", "buttons": 0, "clickCount": 1}, nil)
				browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": point.X, "y": point.Y, "button": "right", "buttons": 2, "clickCount": 1}, nil)
				browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": point.X, "y": point.Y, "button": "right", "buttons": 0, "clickCount": 1}, nil)
				browser.waitFor("native context menu", `String(document.getElementById('project-tab-menu').matches(':popover-open'))+':'+String(!document.getElementById('project-tab-pin-action'))`, "true:true")
				browser.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Escape", "code": "Escape"}, nil)
				browser.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "Escape", "code": "Escape"}, nil)
				browser.waitFor("dismiss context menu", `String(document.getElementById('project-tab-menu').matches(':popover-open'))`, "false")
				if got := browser.evaluate(`String(!document.getElementById('project-settings-btn') && getComputedStyle(document.querySelector('[data-project-tab="p00"]').parentElement,'::before').backgroundImage.includes('radial-gradient'))`); got != "true" {
					t.Fatal("desktop gear removed and active curved joins required", got)
				}
				browser.evaluate(`(function(){document.querySelector('[data-project-tab="p01"]').focus();return 'focused';})()`)
				browser.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "F10", "code": "F10", "modifiers": 8}, nil)
				browser.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "F10", "code": "F10", "modifiers": 8}, nil)
				browser.click("#project-tab-settings-action")
				browser.waitFor("settings belong to clicked inactive tab", `document.getElementById('settings-project')?.textContent || ''`, "/projects/p01/edit")
				browser.waitFor("settings do not switch projects", `document.getElementById('project-selector').value`, "p00")

				if got := browser.evaluate(`(function(){var bar=document.getElementById('desktop-project-titlebar').getBoundingClientRect(),tab=document.querySelector('.desktop-project-tab').getBoundingClientRect();return String(tab.width>=240 && tab.top-bar.top>=5);})()`); got != "true" {
					t.Fatal("wide inset tabs must fit the titlebar", got)
				}
				if runtime.GOOS == "darwin" {
					// AppKit draws macOS controls above the webview; HTML replicas
					// would duplicate them. Their behavior is tested natively.
					if got := browser.evaluate(`String(document.querySelector('#desktop-project-titlebar .desktop-window-controls, #desktop-project-titlebar [data-wml-window]') === null)`); got != "true" {
						t.Fatal("macOS titlebar must not duplicate native window controls in HTML", got)
					}
				} else {
					if got := browser.evaluate(`(function(){var bar=document.getElementById('desktop-project-titlebar').getBoundingClientRect(),controls=document.querySelector('.desktop-window-controls');return String(!!controls && controls.querySelectorAll('button').length===3 && Array.from(controls.querySelectorAll('button')).every(function(button){var r=button.getBoundingClientRect();return Math.abs((r.top+r.bottom-bar.top-bar.bottom)/2)<1 && getComputedStyle(button).getPropertyValue('--wails-draggable').trim()==='no-drag';}));})()`); got != "true" {
						t.Fatal("HTML window controls must be vertically centered and non-draggable", got)
					}
				}

				if got := browser.evaluate(`String(document.getElementById('desktop-project-tabs').scrollWidth > document.getElementById('desktop-project-tabs').clientWidth && document.documentElement.scrollWidth <= innerWidth)`); got != "true" {
					t.Fatal("tabs must overflow within their own scrollport", got, browser.evaluate(`JSON.stringify([document.getElementById("desktop-project-tabs").scrollWidth,document.getElementById("desktop-project-tabs").clientWidth,document.documentElement.scrollWidth,innerWidth])`))
				}
				if got := browser.evaluate(`JSON.stringify([getComputedStyle(document.querySelector('.desktop-titlebar-space')).getPropertyValue('--wails-draggable').trim(),getComputedStyle(document.querySelector('[data-project-tab]')).getPropertyValue('--wails-draggable').trim(),getComputedStyle(document.getElementById('project-selector-trigger')).getPropertyValue('--wails-draggable').trim()])`); got != `["drag","no-drag","no-drag"]` {
					t.Fatal("drag regions", got)
				}
				if got := browser.evaluate(`(function(){var active=document.querySelector('[data-project-tab="p00"]'),inactive=document.querySelector('[data-project-tab="p01"]');return String(getComputedStyle(active.parentElement).backgroundColor===getComputedStyle(document.body).backgroundColor && getComputedStyle(active.parentElement).backgroundColor!==getComputedStyle(inactive.parentElement).backgroundColor && getComputedStyle(active).backgroundColor==='rgba(0, 0, 0, 0)' && active.getBoundingClientRect().width===active.parentElement.getBoundingClientRect().width);})()`); got != "true" {
					t.Fatal("active background must cover the whole tab, not only the label", got)
				}
				if got := browser.evaluate(`(function(){var plus=document.getElementById('project-selector-trigger').getBoundingClientRect(),close=document.querySelector('[data-close-project]').getBoundingClientRect();return String(Math.abs((plus.top+plus.bottom-close.top-close.bottom)/2)<0.5);})()`); got != "true" {
					t.Fatal("plus button must align vertically with tab close buttons", got)
				}

				if got := browser.evaluate(`(function(){var button=document.getElementById('project-selector-trigger'),icon=button.querySelector('svg');if(!icon)return 'missing icon';var a=button.getBoundingClientRect(),b=icon.getBoundingClientRect();return String(Math.abs(a.left+a.right-b.left-b.right)<1 && Math.abs(a.top+a.bottom-b.top-b.bottom)<1);})()`); got != "true" {
					t.Fatal("plus icon must be centered inside its hover circle", got)
				}

				if got := browser.evaluate(`(function(){var tab=document.querySelector('[data-project-tab]'),nav=document.querySelector('[data-nav-base="/tasks"]'),a=getComputedStyle(tab),b=getComputedStyle(nav),plus=document.getElementById('project-selector-trigger'),icon=plus.querySelector('svg'),container=plus.closest('[data-project-selector]');return String(a.fontFamily===b.fontFamily && a.fontSize===b.fontSize && a.lineHeight===b.lineHeight && icon.getBoundingClientRect().width===12 && Math.abs(icon.getBoundingClientRect().left-container.getBoundingClientRect().left-16)<0.5);})()`); got != "true" {
					t.Fatal("tab typography must match sidebar and plus must use label inset with a compact icon", got)
				}

				if got := browser.evaluate(`(function(){return String(Array.from(document.querySelectorAll('.desktop-project-tab')).filter(function(tab){return !tab.querySelector('[aria-selected="true"]');}).every(function(tab){var separator=getComputedStyle(tab,'::after'),rect=tab.getBoundingClientRect(),close=tab.querySelector('[data-close-project]').getBoundingClientRect();return Math.abs(rect.top+parseFloat(separator.top)-(close.top+close.bottom)/2)<0.5;}));})()`); got != "true" {
					t.Fatal("inactive separators must share the close buttons' vertical center", got)
				}

				// A fullscreen-sized content viewport must retain the same visible bar.				// Native macOS fullscreen transitions require separate Wails verification.
				browser.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1920, "height": 1080, "deviceScaleFactor": 1, "mobile": false}, nil)
				if got := browser.evaluate(`(function(){var tab=document.querySelector('.desktop-project-tab').getBoundingClientRect();return String(tab.top>=5 && tab.bottom<=46);})()`); got != "true" {
					t.Fatal("fullscreen-sized viewport lost window controls or tabs", got)
				}
				browser.call("Emulation.clearDeviceMetricsOverride", map[string]any{}, nil)
				browser.click(`[data-nav-base="/schedule"]`)
				browser.waitFor("project A schedule", `location.pathname`, "/schedule")
				browser.click(`[data-project-tab="p01"]`)
				browser.waitFor("new project starts independently", `location.pathname+location.search`, "/chat?project_id=p01")
				if got := browser.evaluate(`(function(){var tab=document.querySelector('.desktop-project-tab'),left=getComputedStyle(tab,'::before');return String(left.content==='none');})()`); got != "true" {
					t.Fatal("first inactive tab must not have a leading separator", got)
				}
				browser.click(`[data-nav-base="/tasks"]`)
				browser.waitFor("project B tasks", `location.pathname`, "/tasks")
				browser.click(`[data-project-tab="p00"]`)
				browser.waitFor("project A remembers schedule", `location.pathname+location.search`, "/schedule?project_id=p00")
				browser.click(`[data-project-tab="p01"]`)
				browser.waitFor("project B remembers tasks", `location.pathname+location.search`, "/tasks?project_id=p01")
				browser.click("#filtered-page")
				browser.waitFor("project B filters", `location.search+location.hash`, "?project_id=p01&view=board#keep")
				browser.waitFor("filtered page shell ready", `String(document.readyState === 'complete' && !!document.querySelector('[data-project-tab="p00"]'))`, "true")
				browser.click(`[data-project-tab="p00"]`)
				browser.waitFor("project A unaffected by B filters", `location.pathname+location.search`, "/schedule?project_id=p00")
				browser.evaluate(`String(window.beforeProjectTabsReload = true)`)
				browser.reload()
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
				browser.click(`[data-close-project="p02"]`)
				browser.waitFor("closing active tab chooses neighbor", `location.search`, "?project_id=p03")
				browser.waitFor("closed tab", `String(!document.querySelector('[data-project-tab="p02"]'))`, "true")
				key("Home", "Home")
				browser.click("#project-selector-trigger")
				browser.waitFor("full selector", `String(document.getElementById('project-selector-dialog').open)`, "true")

				if got := browser.evaluate(`String(Math.abs(document.getElementById('new-project-btn').getBoundingClientRect().height - document.querySelector('[data-project-selector-option]').getBoundingClientRect().height) < 1 && document.getElementById('new-project-btn').parentElement.nextElementSibling.matches('[role="separator"]'))`); got != "true" {
					t.Fatal("create row must match option height and have a separator", got)
				}
				var plusPoint struct{ X, Y float64 }
				if err := json.Unmarshal([]byte(browser.evaluate(`JSON.stringify((function(){var r=document.getElementById('project-selector-trigger').getBoundingClientRect();return {X:r.x+r.width/2,Y:r.y+r.height/2};})())`)), &plusPoint); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 4; i++ {
					browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": plusPoint.X, "y": plusPoint.Y}, nil)
					browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": plusPoint.X, "y": plusPoint.Y, "button": "left", "buttons": 1, "clickCount": 1}, nil)
					browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": plusPoint.X, "y": plusPoint.Y, "button": "left", "buttons": 0, "clickCount": 1}, nil)
					if got := browser.evaluate(`String(getComputedStyle(document.getElementById('project-selector-trigger')).backgroundColor !== 'rgba(0, 0, 0, 0)')`); got != "true" {
						t.Fatal("plus highlight flashes off when toggling menu")
					}
				}
				browser.click("#project-selector-search")
				if got := browser.evaluate(`String(getComputedStyle(document.getElementById('project-selector-trigger')).backgroundColor !== 'rgba(0, 0, 0, 0)')`); got != "true" {
					t.Fatal("plus must stay highlighted while its menu is open and pointer leaves")
				}
				browser.typeText("Project p02")
				browser.click(`[data-project-selector-option][data-project-id="p02"]`)
				browser.waitFor("selector reopens a closed project tab", `String(location.search === '?project_id=p02' && !!document.querySelector('[data-project-tab="p02"][aria-selected="true"]'))`, "true")
				browser.waitFor("opened tab automatically saved", `String(JSON.parse(document.getElementById('desktop-project-titlebar').dataset.pinnedProjects).includes('p02'))`, "true")
				browser.evaluateAwait(`new Promise(resolve => { function check() { fetch('/tasks').then(r=>r.text()).then(html=> { if (html.includes('data-project-tab="p02"')) resolve('saved'); else setTimeout(check,20); }); } check(); })`)
				// History restoration updates both tab selection and sidebar URLs.
				browser.navigateHistory(-1)
				browser.waitFor("history project", `document.getElementById('project-selector').value`, "p03")
				browser.waitFor("history sidebar scope", `String(Array.from(document.querySelectorAll('[data-nav-base]')).every(a=>a.getAttribute('href').includes('project_id=p03')))`, "true")
				browser.click("#project-selector-trigger")
				browser.waitFor("add menu opens", `String(document.getElementById('project-selector-dialog').open)`, "true")
				browser.click("#project-selector-search")
				browser.typeText("no matching project")
				browser.click("#new-project-btn")
				browser.waitFor("create action loads existing project form route", `String(!!document.querySelector('#new-project-container #create-project-fixture') && !document.getElementById('project-selector-dialog').open)`, "true")

			})
			if desktop {
				mu.Lock()
				expected := strings.Join(pins, ",")
				selected = "p00"
				mu.Unlock()
				// A new browser process/profile has no prior sessionStorage or DOM.
				runComposerFocusCDP(t, chrome, server.URL+"/chat", "project-tabs-restart", func(browser *composerFocusCDP) {
					browser.waitFor("active page restored after fresh startup", `location.pathname + location.search`, "/schedule?project_id=p00")
					browser.waitFor("opened tabs restored in saved order after restart", `Array.from(document.querySelectorAll('[data-project-tab]')).map(tab=>tab.dataset.projectTab).join(',')`, expected)
					browser.evaluate(`(function(){document.querySelector('[data-project-tab="p02"]').click();return 'selected';})()`)
					browser.waitFor("select tab to keep", `document.getElementById('project-selector').value`, "p02")
					browser.waitFor("selected tab navigation", `location.search`, "?project_id=p02")
					browser.evaluate(`(function(){var ids=Array.from(document.querySelectorAll('[data-close-project]')).map(button=>button.dataset.closeProject).filter(id=>id!=='p02');ids.forEach(id=>document.querySelector('[data-close-project="'+id+'"]').click());return 'closed';})()`)
					browser.waitFor("last tab cannot close", `String(document.querySelectorAll('[data-project-tab]').length === 1 && document.querySelector('[data-close-project]').disabled)`, "true")
					browser.waitFor("one tab does not overflow", `(function(){var list=document.getElementById('desktop-project-tabs');return String(list.scrollWidth===list.clientWidth && list.scrollHeight===list.clientHeight);})()`, "true")
					browser.waitFor("tab strip suppresses native scrollbar chrome", `(function(){var style=getComputedStyle(document.getElementById('desktop-project-tabs'));return style.overflowY+':'+style.scrollbarWidth;})()`, "hidden:none")
					browser.evaluate(`(function(){document.querySelector('[data-close-project]').click(); document.querySelector('[data-project-tab]').dispatchEvent(new KeyboardEvent('keydown', {key: 'Delete', bubbles: true}));return 'attempted';})()`)
					browser.waitFor("last tab survives click and Delete", `String(document.querySelectorAll('[data-project-tab]').length === 1)`, "true")
				})
			}
			mu.Lock()
			final := strings.Join(pins, ",")
			mu.Unlock()
			if desktop && !strings.Contains(final, "p02") {
				t.Fatal("opened tab not persisted", final)
			}
		})
	}
}
