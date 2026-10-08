package pages

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_PopoverCompatibility(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	legacyCSS := strings.NewReplacer(":popover-open", ":unsupported-popover-open", ":has(", ":unsupported-has(", "oklch(", "unsupported-oklch(", "color-mix(", "unsupported-color-mix(", ":focus-visible", ":unsupported-focus-visible")
	// Linux headless Chrome can report no hover-capable primary input even
	// when CDP dispatches mouse events. Keep this coverage independent of the
	// host's media query result by disabling the library's hover media rules.
	noHoverCSS := strings.NewReplacer("(hover:hover)", "(hover:unsupported-hover)", "(hover: hover)", "(hover: unsupported-hover)")
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint("legacy=", legacy), func(t *testing.T) {
			project := models.Project{ID: "compat-project", Name: "Compatibility"}
			other := models.Project{ID: "other-project", Name: "Other project"}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, ".css") {
					recorder := httptest.NewRecorder()
					if static.ServeAsset(recorder, r) {
						for key, values := range recorder.Header() {
							if key != "Content-Length" {
								w.Header()[key] = values
							}
						}
						w.WriteHeader(recorder.Code)
						css := noHoverCSS.Replace(recorder.Body.String())
						if legacy {
							css = legacyCSS.Replace(css)
						}
						fmt.Fprint(w, css)
						return
					}
				}
				if static.ServeAsset(w, r) {
					return
				}
				if r.URL.Path == "/wails/runtime.js" {
					w.Header().Set("Content-Type", "text/javascript")
					fmt.Fprint(w, `export const Window = {};`)
					return
				}
				if r.URL.Path != "/tasks/new" && r.URL.Path != "/schedule" {
					w.WriteHeader(204)
					return
				}
				var page bytes.Buffer
				ctx := layout.WithUIPreferences(layout.WithDesktopMode(r.Context(), true), layout.UIPreferences{PinnedProjectIDs: []string{project.ID, other.ID}})
				component := NewTask([]models.Project{project, other}, &project, nil, nil)
				if r.URL.Path == "/schedule" {
					component = Schedule([]models.Project{project, other}, &project, nil, 0, nil, nil)
				}
				if err := component.Render(ctx, &page); err != nil {
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
				fmt.Fprint(&icons, `<div id="menu-style-fixture">`)
				action := components.CardActionDropdown(components.CardActionDropdownConfig{Label: "Style reference"})
				if err := action.Render(templ.WithChildren(r.Context(), templ.Raw(`<li><button type="button" class="active">Action</button></li><li><button type="button" class="text-error">Delete</button></li><li><a href="#" class="text-error">Delete alerts</a></li><li><button disabled>Unavailable</button></li><li><form><button class="ov-menu-option w-full" type="submit">Form action</button></form></li>`)), &icons); err != nil {
					t.Error(err)
					return
				}
				fmt.Fprint(&icons, `</div>`)
				if err := components.TaskCard(models.Task{ID: "hidden-retry-fixture", Title: "Retry visibility", Category: models.CategoryBacklog, Status: models.StatusPending}, project.ID, "backlog", nil, nil).Render(r.Context(), &icons); err != nil {
					t.Error(err)
					return
				}

				html := strings.Replace(page.String(), "</body>", icons.String()+"</body>", 1)
				if legacy {
					// Emulate missing methods and an unrecognized pseudo-class, including CSS parsing.
					html = legacyCSS.Replace(html)
					html = strings.Replace(html, "<head>", `<head><script>delete HTMLElement.prototype.showPopover; delete HTMLElement.prototype.hidePopover;</script>`, 1)
				}
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprint(w, html)
			}))
			defer server.Close()
			runComposerFocusCDP(t, chrome, server.URL+"/tasks/new?tab=details", "popover-compat", func(b *composerFocusCDP) {
				hover := func(selector string) {
					var point struct{ X, Y float64 }
					if err := json.Unmarshal([]byte(b.evaluate(fmt.Sprintf(`JSON.stringify((function(){var e=document.querySelector(%q);e.scrollIntoView({block:'nearest'});var r=e.getBoundingClientRect();return {X:r.left+r.width/2,Y:r.top+r.height/2}})())`, selector))), &point); err != nil {
						t.Fatal(err)
					}
					b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": point.X, "y": point.Y}, nil)
				}
				b.waitFor("task panel", `String(!!document.querySelector('[data-detail-property="priority"]'))`, "true")
				checkStableHover := func(selector, rootSelector, optionsSelector string) {
					t.Helper()
					result := b.evaluate(fmt.Sprintf(`(function(){var row=document.querySelector(%q),root=row.closest(%q);row.dispatchEvent(new PointerEvent('pointermove',{bubbles:true}));var original=root.querySelectorAll,calls=0;root.querySelectorAll=function(selector){if(selector===%q)calls++;return original.call(this,selector)};try{for(var i=0;i<10;i++)row.dispatchEvent(new PointerEvent('pointermove',{bubbles:true}));return String(calls)}finally{delete root.querySelectorAll}})()`, selector, rootSelector, optionsSelector))
					if result != "0" {
						t.Fatalf("repeated hover scans options for %s: %s", selector, result)
					}
				}
				applyTheme := func(theme string) {
					b.waitFor("theme switch ready", `typeof window.applyOpenVibelyTheme`, "function")
					b.evaluate(fmt.Sprintf(`window.applyOpenVibelyTheme(%q, false); 'ok'`, "openvibely-"+theme))
					b.waitFor("theme state", `document.documentElement.dataset.theme + ':' + document.documentElement.dataset.colorTheme`, theme+":openvibely-"+theme)
				}
				for _, theme := range []string{"dark", "light"} {
					applyTheme(theme)
					b.waitFor("Retry initially hidden "+theme, `getComputedStyle(document.querySelector('#task-hidden-retry-fixture [data-task-card-merge-options-retry]')).display`, "none")
					for _, mode := range []string{"loading", "error", "idle"} {
						b.evaluate(fmt.Sprintf(`setTaskCardMergeOptionsStatus(document.querySelector('#task-hidden-retry-fixture [data-task-card-merge-options]'), %q, null, null); 'ok'`, mode))
						want := "none"
						if mode == "error" {
							want = "flex"
						}
						b.waitFor("Retry visibility "+theme+" "+mode, `getComputedStyle(document.querySelector('#task-hidden-retry-fixture [data-task-card-merge-options-retry]')).display`, want)
					}

					b.waitFor("task panel divider", `String(getComputedStyle(document.querySelector('#task-details-panel > [role="tablist"]')).boxShadow !== 'none')`, "true")
					b.click(`#task-thread-form-agent-select`)
					b.waitFor("composer menu surface", `String(getComputedStyle(document.getElementById('conversation-model-picker')).backgroundColor !== 'rgba(0, 0, 0, 0)' && getComputedStyle(document.getElementById('conversation-model-picker')).borderTopWidth === '1px')`, "true")
					b.waitFor("shared menu surfaces "+theme, `(function(){var model=getComputedStyle(document.getElementById('conversation-model-picker'));return String(['#project-selector-dialog','#project-tab-menu','#task-property-picker','#menu-style-fixture .dropdown-content'].every(function(selector){var s=getComputedStyle(document.querySelector(selector));return ['backgroundColor','borderTopWidth','borderTopColor','borderRadius','boxShadow','fontSize'].every(function(key){return s[key]===model[key]})}))})()`, "true")
					b.waitFor("selected and destructive menu colors "+theme, `(function(){var normal=getComputedStyle(document.querySelector('.ov-mp-pick')).color;return String(getComputedStyle(document.querySelector('#menu-style-fixture .active')).color===normal && getComputedStyle(document.querySelector('#menu-style-fixture button.text-error')).color!==normal && getComputedStyle(document.querySelector('#menu-style-fixture a.text-error')).color===getComputedStyle(document.querySelector('#menu-style-fixture button.text-error')).color)})()`, "true")
					b.waitFor("shared menu row spacing "+theme, `(function(){var model=getComputedStyle(document.querySelector('.ov-mp-pick'));return String(['#project-selector-dialog [data-searchable-selector-option]','#menu-style-fixture li button','#menu-style-fixture form button','#project-tab-settings-action','#task-property-picker button[data-value]'].every(function(selector){var s=getComputedStyle(document.querySelector(selector));return ['paddingTop','paddingBottom','paddingLeft','paddingRight','fontSize','borderRadius'].every(function(key){return s[key]===model[key]})}))})()`, "true")
					b.waitFor("shared search headers "+theme, `(function(){var model=getComputedStyle(document.querySelector('.ov-mp-search'));return String(Array.from(document.querySelectorAll('[data-searchable-selector-search-shell],#task-property-picker .ov-menu-search')).every(function(el){var s=getComputedStyle(el);return ['padding','borderBottomWidth','borderBottomColor'].every(function(key){return s[key]===model[key]})}))})()`, "true")
					b.waitFor("composer active-row color", `(function(){var row=document.querySelector('#conversation-model-picker .ov-mp-row');row.setAttribute('data-picker-active','');return String(getComputedStyle(row).backgroundColor!=='rgba(0, 0, 0, 0)')})()`, "true")
					b.click(`.project-bar-brand`)
					b.evaluate(`var panel=document.querySelector('#menu-style-fixture .dropdown-content').cloneNode(true);panel.id='submenu-hover-fixture';panel.classList.remove('dropdown-content');panel.setAttribute('data-task-card-submenu-portaled','true');panel.style.cssText='display:block;visibility:visible;opacity:1;position:fixed;left:300px;top:180px;z-index:2000';panel.querySelector('.active').classList.remove('active');document.body.append(panel);'ok'`)
					hover(`#submenu-hover-fixture button:not(:disabled)`)
					b.waitFor("enabled submenu hover "+theme, `String(getComputedStyle(document.querySelector('#submenu-hover-fixture button:not(:disabled)')).backgroundColor !== 'rgba(0, 0, 0, 0)')`, "true")
					hover(`#submenu-hover-fixture button:disabled`)
					b.waitFor("disabled submenu appearance "+theme, `(function(){var s=getComputedStyle(document.querySelector('#submenu-hover-fixture button:disabled'));return [s.opacity,s.cursor,s.backgroundColor].join('|')})()`, "0.45|not-allowed|rgba(0, 0, 0, 0)")
					b.evaluate(`document.getElementById('submenu-hover-fixture').remove();'ok'`)
					b.click(`#project-selector-trigger`)
					hover(`#project-selector-dialog [data-searchable-selector-option]`)
					b.waitFor("project option highlighted", `String(!!document.querySelector('#project-selector-dialog [data-selector-active]'))`, "true")
					checkStableHover("#project-selector-dialog [data-searchable-selector-option]", "[data-searchable-selector]", "[data-searchable-selector-option]")
					hover(`#project-selector-dialog input`)
					b.waitFor("project search clears highlight", `String(!document.querySelector('#project-selector-dialog [data-selector-active]'))`, "true")
					b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Escape", "code": "Escape", "windowsVirtualKeyCode": 27}, nil)
					b.waitFor("project menu closed", `String(!document.getElementById('project-selector-dialog').open)`, "true")

					b.evaluate(`var source=document.createElement('a');source.id='compat-search-source';source.className='stream-web-search-source';source.textContent='Search result';source.style.cssText='position:fixed;left:300px;top:180px;z-index:1000';document.body.append(source);'ok'`)
					hover(`#compat-search-source`)
					b.waitFor("search source border and hover", `String(getComputedStyle(document.getElementById('compat-search-source')).borderTopWidth === '1px' && getComputedStyle(document.getElementById('compat-search-source')).backgroundColor !== 'rgba(0, 0, 0, 0)')`, "true")
					b.evaluate(`document.getElementById('compat-search-source').remove();'ok'`)
					hover(`[data-detail-property="priority"]`)
					b.waitFor("task property hover", `String(getComputedStyle(document.querySelector('[data-detail-property="priority"]')).backgroundColor !== 'rgba(0, 0, 0, 0)')`, "true")
					hover(`#inspector-tab-schedules`)
					b.waitFor("panel tabs have no hover highlight", `getComputedStyle(document.getElementById('inspector-tab-schedules')).backgroundColor`, "rgba(0, 0, 0, 0)")
					if legacy {
						hover(`#task-panel-divider`)
						b.waitFor("resize divider hover", `getComputedStyle(document.getElementById('task-panel-divider'),'::after').opacity`, "0.45")
					}
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
					checkStableHover("#task-property-picker [data-options=priority] [data-value='4']", "#task-property-picker", "button[data-value]")
					b.click(`[data-options="priority"] [data-value="4"]`)
					b.waitFor("selection saved", `document.querySelector('[data-draft-property="priority"]').value`, "4")
					b.waitFor("picker closes after selection", `String(document.getElementById('task-property-picker').getClientRects().length === 0)`, "true")
					b.click(`[data-detail-property="agent_id"]`)
					for _, state := range []string{"isComposing", "keyCode229", "defaultPrevented"} {
						result := b.evaluate(fmt.Sprintf(`(function(){
							var state=%q, picker=document.getElementById('task-property-picker'), input=picker.querySelector('input');
							input.value='agent search';
							var event=new KeyboardEvent('keydown', {key:'Escape', bubbles:true, cancelable:true, isComposing:state==='isComposing', keyCode:state==='keyCode229'?229:27});
							if(state==='defaultPrevented')event.preventDefault();
							input.dispatchEvent(event);
							return String(picker.getClientRects().length>0 && document.activeElement===input && input.value==='agent search' && event.defaultPrevented===(state==='defaultPrevented'));
						})()`, state))
						if result != "true" {
							t.Fatalf("Escape must preserve picker and search focus for %s: got %s", state, result)
						}
					}
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
					b.waitFor("menu surface and border "+theme, `(function(){var s=getComputedStyle(document.getElementById('project-tab-menu'));return String(s.backgroundColor!=='rgba(0, 0, 0, 0)' && s.borderTopWidth==='1px' && s.borderTopStyle==='solid' && s.borderTopColor!=='rgba(0, 0, 0, 0)')})()`, "true")
					hover(`#project-tab-settings-action`)
					b.waitFor("menu hover "+theme, `String(getComputedStyle(document.getElementById('project-tab-settings-action')).backgroundColor !== 'rgba(0, 0, 0, 0)')`, "true")
					if legacy && theme == "dark" {
						b.waitFor("legacy dark menu background", `getComputedStyle(document.getElementById('project-tab-menu')).backgroundColor`, "rgb(29, 35, 42)")
						b.waitFor("legacy dark menu border", `getComputedStyle(document.getElementById('project-tab-menu')).borderTopColor`, "rgba(166, 173, 187, 0.2)")
					}
					if theme == "light" {
						b.waitFor("light menu surface and border", `(function(){var s=getComputedStyle(document.getElementById('project-tab-menu'));return String(s.backgroundColor==='rgb(250, 250, 250)' && s.borderTopWidth==='1px' && s.borderTopStyle==='solid' && s.borderTopColor===getComputedStyle(document.getElementById('conversation-model-picker')).borderTopColor)})()`, "true")
						b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 230, "y": 60}, nil)
						b.waitFor("light menu hover highlight", `getComputedStyle(document.getElementById('project-tab-settings-action')).backgroundColor`, "rgb(232, 232, 232)")
					}
					b.evaluate(`document.getElementById('project-tab-settings-action').dispatchEvent(new KeyboardEvent('keydown', {key:'Escape', bubbles:true})); 'ok'`)
					b.waitFor("tab menu dismissed", `String(!document.getElementById('project-tab-menu').getClientRects().length)`, "true")
				}
				b.call("Page.navigate", map[string]any{"url": server.URL + "/schedule"}, nil)
				b.waitFor("schedule context menu", `String(!!document.getElementById('schedule-context-menu'))`, "true")
				for _, theme := range []string{"dark", "light"} {
					applyTheme(theme)
					b.evaluate(`var menu=document.getElementById('schedule-context-menu');menu.classList.remove('hidden');menu.style.left='300px';menu.style.top='200px'; 'ok'`)
					hover(`[data-schedule-context-action="run"]`)
					b.waitFor("schedule menu hover "+theme, `String(getComputedStyle(document.querySelector('[data-schedule-context-action="run"]')).backgroundColor !== 'rgba(0, 0, 0, 0)')`, "true")
				}
			})
		})
	}
}
