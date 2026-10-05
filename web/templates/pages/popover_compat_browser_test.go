package pages

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_PopoverCompatibility(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint("legacy=", legacy), func(t *testing.T) {
			project := models.Project{ID: "compat-project", Name: "Compatibility"}
			other := models.Project{ID: "other-project", Name: "Other project"}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if static.ServeAsset(w, r) {
					return
				}
				if r.URL.Path == "/wails/runtime.js" {
					w.Header().Set("Content-Type", "text/javascript")
					fmt.Fprint(w, `export const Window = {};`)
					return
				}
				if r.URL.Path != "/tasks/new" {
					w.WriteHeader(204)
					return
				}
				var page bytes.Buffer
				ctx := layout.WithUIPreferences(layout.WithDesktopMode(r.Context(), true), layout.UIPreferences{PinnedProjectIDs: []string{project.ID, other.ID}})
				if err := NewTask([]models.Project{project, other}, &project, nil, nil).Render(ctx, &page); err != nil {
					t.Error(err)
					return
				}
				var icons bytes.Buffer
				fmt.Fprint(&icons, `<div id="status-color-fixture" class="menu"><a>`)
				for _, status := range []models.TaskStatus{models.StatusQueued, models.StatusRunning, models.StatusCompleted, models.StatusFailed, models.StatusBlocked, models.StatusCancelled} {
					if err := components.TaskStateIcon(models.Task{Status: status}).Render(r.Context(), &icons); err != nil {
						t.Error(err)
					}
				}
				fmt.Fprint(&icons, `</a></div>`)
				html := strings.Replace(page.String(), "</body>", icons.String()+"</body>", 1)
				if legacy {
					// Emulate missing methods and an unrecognized pseudo-class, including CSS parsing.
					html = strings.ReplaceAll(html, ":popover-open", ":unsupported-popover-open")
					html = strings.ReplaceAll(html, ":has(", ":unsupported-has(")
					html = strings.ReplaceAll(html, "oklch(", "unsupported-oklch(")
					html = strings.Replace(html, "<head>", `<head><script>delete HTMLElement.prototype.showPopover; delete HTMLElement.prototype.hidePopover;</script>`, 1)
				}
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprint(w, html)
			}))
			defer server.Close()
			runComposerFocusCDP(t, chrome, server.URL+"/tasks/new?tab=details", "popover-compat", func(b *composerFocusCDP) {
				b.waitFor("task panel", `String(!!document.querySelector('[data-detail-property="priority"]'))`, "true")
				for _, theme := range []string{"dark", "light"} {
					b.evaluate(fmt.Sprintf(`document.documentElement.setAttribute('data-theme', %q); 'ok'`, theme))
					if theme == "light" {
						b.waitFor("light title bar background", `getComputedStyle(document.getElementById('desktop-project-titlebar')).backgroundColor`, "rgb(232, 232, 232)")
						var point struct{ X, Y float64 }
						if err := json.Unmarshal([]byte(b.evaluate(`JSON.stringify((function(){var r=document.getElementById('project-selector-trigger').getBoundingClientRect();return {X:r.left+r.width/2,Y:r.top+r.height/2}})())`)), &point); err != nil {
							t.Fatal(err)
						}
						b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": point.X, "y": point.Y}, nil)
						b.waitFor("light add-project hover", `getComputedStyle(document.getElementById('project-selector-trigger')).backgroundColor`, "rgb(206, 206, 206)")
						for _, hover := range []struct{ Selector, Color string }{
							{`[data-project-tab="other-project"]`, "rgba(38, 38, 39, 0.08)"},
							{`[data-close-project="other-project"]`, "rgba(38, 38, 39, 0.18)"},
						} {
							if err := json.Unmarshal([]byte(b.evaluate(fmt.Sprintf(`JSON.stringify((function(){var r=document.querySelector(%q).getBoundingClientRect();return {X:r.left+r.width/2,Y:r.top+r.height/2}})())`, hover.Selector))), &point); err != nil {
								t.Fatal(err)
							}
							b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": point.X, "y": point.Y}, nil)
							b.waitFor("inactive tab hover", fmt.Sprintf(`getComputedStyle(document.querySelector(%q)).backgroundColor`, hover.Selector), hover.Color)
							b.waitFor("tab stays highlighted over close button", `getComputedStyle(document.querySelector('[data-project-tab="other-project"]')).backgroundColor`, "rgba(38, 38, 39, 0.08)")
						}
						b.waitFor("light task status colors", `(function(){var expected={queued:'rgb(37, 99, 235)',running:'rgb(116, 128, 255)',completed:'rgb(46, 160, 67)',failed:'rgb(248, 81, 73)',blocked:'rgb(137, 85, 3)',cancelled:'rgb(118, 118, 118)'};return String(Array.from(document.querySelectorAll('#status-color-fixture [data-task-state-icon]')).every(function(icon){return getComputedStyle(icon).color===expected[icon.dataset.taskState] && getComputedStyle(icon.querySelector('svg')).stroke===expected[icon.dataset.taskState];}))})()`, "true")
						b.waitFor("active tab connects to content", `(function(){var tab=document.querySelector('[data-project-tab][aria-selected="true"]').parentElement, style=getComputedStyle(tab), bar=document.getElementById('desktop-project-titlebar');return String(style.backgroundColor===getComputedStyle(document.getElementById('main-content')).backgroundColor && style.backgroundColor!=='rgba(0, 0, 0, 0)' && getComputedStyle(tab,'::before').backgroundImage!=='none' && getComputedStyle(tab,'::after').backgroundImage!=='none' && tab.getBoundingClientRect().bottom===bar.getBoundingClientRect().bottom)})()`, "true")
					}
					b.waitFor("picker initially hidden", `String(document.getElementById('task-property-picker').getClientRects().length === 0)`, "true")
					b.waitFor("tab menu initially hidden", `String(document.getElementById('project-tab-menu').getClientRects().length === 0)`, "true")
					b.click(`[data-detail-property="priority"]`)
					b.waitFor("picker positioned beside property", `(function(){var p=document.getElementById('task-property-picker').getBoundingClientRect(), r=document.querySelector('[data-detail-property="priority"]').getBoundingClientRect();return String(p.width>0 && Math.abs(p.left-r.left)<1 && p.bottom<=innerHeight && p.top>=12)})()`, "true")
					b.waitFor("only priority group visible", `String(Array.from(document.querySelectorAll('#task-property-picker [data-options]')).filter(e=>e.getClientRects().length).map(e=>e.dataset.options).join(',') === 'priority')`, "true")
					b.click(`[data-options="priority"] [data-value="4"]`)
					b.waitFor("selection saved", `document.querySelector('[data-draft-property="priority"]').value`, "4")
					b.waitFor("picker closes after selection", `String(document.getElementById('task-property-picker').getClientRects().length === 0)`, "true")
					b.click(`[data-detail-property="agent_id"]`)
					b.evaluate(`document.querySelector('#task-property-picker input').dispatchEvent(new KeyboardEvent('keydown', {key:'Escape', bubbles:true})); 'ok'`)
					b.waitFor("Escape restores focus", `String(document.activeElement === document.querySelector('[data-detail-property="agent_id"]') && !document.getElementById('task-property-picker').getClientRects().length)`, "true")
					b.click(`[data-detail-property="priority"]`)
					b.click(`[data-detail-property="priority"]`)
					b.waitFor("trigger toggles closed", `String(!document.getElementById('task-property-picker').getClientRects().length)`, "true")
					b.click(`[data-detail-property="priority"]`)
					b.click(`.project-bar-brand`)
					b.waitFor("outside click dismisses picker", `String(!document.getElementById('task-property-picker').getClientRects().length)`, "true")
					b.evaluate(`document.querySelector('[data-project-tab]').dispatchEvent(new MouseEvent('contextmenu', {bubbles:true, clientX:200, clientY:40})); 'ok'`)
					b.waitFor("tab menu opens at pointer", `(function(){var r=document.getElementById('project-tab-menu').getBoundingClientRect();return String(r.width>0 && r.left===200 && r.top===40)})()`, "true")
					if theme == "light" {
						b.waitFor("light menu surface and border", `(function(){var s=getComputedStyle(document.getElementById('project-tab-menu'));return String(s.backgroundColor==='rgb(250, 250, 250)' && s.borderTopWidth==='1px' && s.borderTopStyle==='solid' && s.borderTopColor==='rgb(206, 206, 206)')})()`, "true")
						b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 230, "y": 60}, nil)
						b.waitFor("light menu hover highlight", `getComputedStyle(document.getElementById('project-tab-settings-action')).backgroundColor`, "rgb(232, 232, 232)")
					}
					b.evaluate(`document.getElementById('project-tab-settings-action').dispatchEvent(new KeyboardEvent('keydown', {key:'Escape', bubbles:true})); 'ok'`)
					b.waitFor("tab menu dismissed", `String(!document.getElementById('project-tab-menu').getClientRects().length)`, "true")
				}
			})
		})
	}
}
