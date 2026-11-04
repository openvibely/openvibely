package pages

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_SettingsNavigation(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	var workspaceRequests atomic.Int32
	var failWorkspace atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		var content templ.Component
		switch r.URL.Path {
		case "/tasks/first-thread", "/tasks/second-thread":
			content = templ.Raw(`<div id="task-thread-view" data-task-id="` + r.URL.Path[len("/tasks/"):] + `">Thread</div>`)
		case "/shortcuts":
			content = ShortcutsContent()
		case "/themes":
			content = ThemesContent(nil)
		case "/models", "/agents", "/skills", "/channels", "/personality":
			content = templ.Raw(`<div id="` + r.URL.Path[1:] + `-container">Configuration</div>`)
		case "/workers", "/settings/workers":
			content = WorkerSettingsContent(5, 1, 1, 0, nil, nil)
		case "/tasks":
			workspaceRequests.Add(1)
			if failWorkspace.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			content = templ.Raw(`<div id="settings-workspace"><a id="open-settings" href="/themes" hx-get="/themes" hx-target="#main-content" hx-push-url="true">Settings</a><div id="kanban-board" style="display:block;height:100px;overflow:auto"><div style="height:300px">
<div class="category-drop-zone" data-category="backlog" style="height:60px;overflow:auto"><div style="height:600px">Backlog</div></div>
<div class="task-drop-zone category-drop-zone" data-category="active" data-status="running" style="height:60px;overflow:auto"><div style="height:600px">Running</div></div>
<div class="task-drop-zone category-drop-zone" data-category="active" data-status="pending" style="height:60px;overflow:auto"><div style="height:600px">Queued</div></div>
</div></div></div>`)
		default:
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Header.Get("HX-Request") == "true" {
			if err := content.Render(r.Context(), w); err != nil {
				t.Error(err)
			}
			return
		}
		if err := layout.Base("Settings", []models.Project{{ID: "project", Name: "Project"}}, "project").Render(templ.WithChildren(r.Context(), content), w); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/tasks", "settings-navigation", func(b *composerFocusCDP) {
		// A swapped fragment can be visible before HTMX processes its links. Wait
		// for settlement before clicking again, otherwise a native navigation can
		// discard the workspace return state.
		waitFor := func(label, expression, want string) {
			b.waitFor(label, `(document.readyState === 'complete' && document.documentElement && document.getElementById('main-content') && !document.querySelector('#main-content.htmx-settling, #main-content.htmx-swapping, #settings-content.htmx-settling, #settings-content.htmx-swapping')) ? (`+expression+`) : 'loading'`, want)
		}
		waitFor("workspace ready", `String(document.readyState === 'complete' && !!window.htmx && !!window.syncAppearanceSettings)`, "true")
		b.evaluate(`window.streamClosed=false;window._threadEventSources=[{close(){window.streamClosed=true}}];window._taskThreadStreamingActive=true;window.originalWorkspace=document.getElementById('settings-workspace');window.workspaceDraft=document.createElement('textarea');window.workspaceDraft.value='Unsent draft';window.originalWorkspace.appendChild(window.workspaceDraft);document.getElementById('task-list-toggle').click();document.querySelector('[data-category=backlog]').scrollTop=120;document.querySelector('[data-status=running]').scrollTop=180;document.querySelector('[data-status=pending]').scrollTop=240;document.getElementById('kanban-board').scrollTop=80;document.getElementById('open-settings').click();'opened'`)
		waitFor("themes only", `String(document.documentElement.classList.contains('appearance-settings') && !document.documentElement.classList.contains('task-list-collapsed') && !document.querySelector('[data-settings-section="themes"]').hidden && document.querySelector('[data-settings-section="layout"]').hidden)`, "true")
		waitFor("title bar controls visible", `String(['#task-list-toggle', '#task-list-header .task-list-brand', '#task-list-search-toggle', '#task-list-new'].every(selector => { const el=document.querySelector(selector); return el && el.getBoundingClientRect().width>0 && getComputedStyle(el).visibility==='visible'; }))`, "true")
		waitFor("settings rows clear scrollbar", `(function(){var nav=document.getElementById('appearance-settings-nav'),row=nav.querySelector('.settings-back');return String(nav.getBoundingClientRect().right-row.getBoundingClientRect().right>=16 && nav.scrollWidth<=nav.clientWidth)})()`, "true")
		b.evaluate(`document.getElementById('task-list-toggle').click();'collapse settings'`)
		waitFor("settings can collapse temporarily", `String(document.documentElement.classList.contains('task-list-collapsed') && localStorage.getItem('openvibely.taskListCollapsed')==='true')`, "true")
		b.evaluate(`document.getElementById('task-list-toggle').click();'expand settings'`)
		waitFor("workspace stream preserved", `String(!window.streamClosed && window._taskThreadStreamingActive)`, "true")
		waitFor("workspace retained and hidden", `String(window.originalWorkspace.isConnected && window.originalWorkspace.closest('#settings-retained-workspace').hidden)`, "true")
		b.evaluate(`window.workspaceDraft.value='Updated while Settings is open';'live update'`)
		b.evaluate(`document.querySelector('[data-settings-link="layout"]').click();'layout'`)
		waitFor("layout previews", `String(!document.querySelector('[data-settings-section="layout"]').hidden && document.querySelector('[data-settings-section="themes"]').hidden && !!document.querySelector('[data-settings-section="layout"] .display-preview'))`, "true")
		b.evaluate(`document.querySelector('[data-settings-link="chat"]').click();'chat'`)
		waitFor("chat previews", `String(!document.querySelector('[data-settings-section="chat"]').hidden && !!document.querySelector('[data-settings-section="chat"] .display-preview'))`, "true")
		for _, route := range []string{"models", "shortcuts", "channels", "personality", "skills", "agents"} {
			b.evaluate(`document.querySelector('#appearance-settings-nav [data-nav-base="/` + route + `"] ').click();'configuration'`)
			waitFor("settings navigation survives "+route, `String(!!document.getElementById('`+route+`-container') && document.documentElement.classList.contains('appearance-settings') && !document.documentElement.classList.contains('task-list-collapsed'))`, "true")
			waitFor("settings rail selected", `String(document.querySelector('.sidebar-aside [data-nav-base="/themes"]').getAttribute('aria-current')==='page')`, "true")
			if route == "shortcuts" {
				b.evaluate(`history.back();'back from shortcuts'`)
				waitFor("models before shortcuts", `String(!!document.getElementById('models-container'))`, "true")
				b.evaluate(`history.forward();'forward to shortcuts'`)
				waitFor("shortcuts restored", `String(!!document.getElementById('shortcuts-container'))`, "true")
				b.evaluate(`var shortcutSearch=document.querySelector('[data-shortcut-search]');shortcutSearch.value='steer';shortcutSearch.dispatchEvent(new Event('input'));'search restored page'`)
				waitFor("restored shortcuts search works", `Array.from(document.querySelectorAll('[data-shortcut-row]:not([hidden]) [data-shortcut-action]')).map(el=>el.textContent).join('|')`, "Steer the current turn")
			}
			if route == "models" {
				b.evaluate(`history.back();'back within settings'`)
				waitFor("themes restored by browser back", `String(!!document.getElementById('themes-container'))`, "true")
				waitFor("workspace survives settings history", `String(window.originalWorkspace.isConnected && document.getElementById('settings-workspace')===window.originalWorkspace)`, "true")
				b.evaluate(`history.forward();'forward within settings'`)
				waitFor("models restored by browser forward", `String(!!document.getElementById('models-container') && window.originalWorkspace.isConnected)`, "true")

			}

		}
		b.evaluate(`document.querySelector('#appearance-settings-nav [data-nav-base="/workers"]').click();'limits'`)
		waitFor("worker limits editable", `String(document.documentElement.classList.contains('appearance-settings') && !!document.getElementById('limit-input-global') && document.getElementById('limit-input-global').getBoundingClientRect().width>0)`, "true")
		waitFor("workers only in settings with utilization visible", `String(!document.querySelector('.sidebar-aside [data-nav-base="/workers"]') && Array.from(document.querySelectorAll('#worker-settings-content th')).some(el=>el.textContent==='Running' && el.getBoundingClientRect().width>0))`, "true")
		b.evaluate(`document.querySelector('#appearance-settings-nav [data-settings-link="layout"]').click();'appearance'`)
		waitFor("layout reachable from configuration", `String(!!document.querySelector('[data-settings-section="layout"]') && !document.querySelector('[data-settings-section="layout"]').hidden)`, "true")
		b.evaluate(`document.querySelector('#appearance-settings-nav [data-settings-back]').click();'workspace'`)
		waitFor("workspace restored", `String(!!document.getElementById('settings-workspace') && !document.documentElement.classList.contains('appearance-settings') && document.documentElement.classList.contains('task-list-collapsed'))`, "true")
		waitFor("original workspace and draft restored", `String(document.getElementById('settings-workspace')===window.originalWorkspace && window.workspaceDraft.isConnected && window.workspaceDraft.value==='Updated while Settings is open')`, "true")
		if n := workspaceRequests.Load(); n != 1 {
			t.Fatalf("workspace reloaded on return: %d requests", n)
		}
		waitFor("independent board scroll restored", `JSON.stringify([document.getElementById('kanban-board').scrollTop,document.querySelector('[data-category=backlog]').scrollTop,document.querySelector('[data-status=running]').scrollTop,document.querySelector('[data-status=pending]').scrollTop])`, "[80,120,180,240]")
		b.evaluate(`document.getElementById('open-settings').click();'settings again'`)
		waitFor("settings reopened", `String(document.documentElement.classList.contains('appearance-settings'))`, "true")
		b.evaluate(`document.querySelector('[data-settings-link="chat"]').click();history.back();'back'`)
		waitFor("browser back exits settings", `String(!!document.getElementById('settings-workspace') && !document.documentElement.classList.contains('appearance-settings'))`, "true")
		b.evaluate(`history.forward();'forward to settings'`)
		waitFor("browser forward reopens settings", `String(document.documentElement.classList.contains('appearance-settings') && !!document.getElementById('themes-container'))`, "true")
		b.evaluate(`history.back();'back to retained workspace'`)
		waitFor("browser back restores same workspace", `String(!document.documentElement.classList.contains('appearance-settings') && document.getElementById('settings-workspace')===window.originalWorkspace)`, "true")
		b.evaluate(`document.getElementById('open-settings').click();'settings for search'`)
		waitFor("settings before search", `String(document.documentElement.classList.contains('appearance-settings'))`, "true")
		b.evaluate(`document.getElementById('task-list-search-toggle').click();var input=document.getElementById('task-list-search-input');input.value='themes';input.dispatchEvent(new Event('input'));'search settings'`)
		waitFor("search stays in settings and filters navigation", `String(document.documentElement.classList.contains('appearance-settings') && document.activeElement===document.getElementById('task-list-search-input') && document.querySelector('#appearance-settings-nav [data-settings-link="layout"]').hidden)`, "true")
		b.evaluate(`document.getElementById('task-list-search-input').dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));document.querySelector('#appearance-settings-nav [data-settings-back]').click();'back after search'`)
		waitFor("workspace restored after settings search", `String(!!document.getElementById('settings-workspace') && !document.documentElement.classList.contains('appearance-settings'))`, "true")

		if n := workspaceRequests.Load(); n != 1 {
			t.Fatalf("workspace reloaded by Back or Search: %d requests", n)
		}
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 390, "height": 844, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.evaluate(`document.getElementById('open-settings').click();'mobile settings'`)
		waitFor("mobile navigation visible", `String(!!document.querySelector('.settings-mobile-nav') && document.querySelector('.settings-mobile-nav').getBoundingClientRect().height > 0)`, "true")
		b.evaluate(`document.querySelector('.settings-mobile-nav [data-settings-link="layout"]').click();'mobile layout'`)
		waitFor("mobile layout preview visible", `String(document.querySelector('[data-settings-section="layout"] .display-preview').getBoundingClientRect().height > 0)`, "true")
		b.evaluate(`htmx.ajax('GET','/workers',{target:'#main-content',swap:'innerHTML'});'monitor'`)
		waitFor("workers remain in settings", `String(!!document.getElementById('worker-settings-content') && document.documentElement.classList.contains('appearance-settings') && !!document.getElementById('limit-input-global') && document.getElementById('limit-input-global').getBoundingClientRect().width>0)`, "true")

		// Project tabs share the shell, but each Settings return belongs to
		// the workspace in that project, including its filters and hash.
		b.evaluate(`htmx.ajax('GET','/tasks?project_id=first&category=backlog',{target:'#main-content',swap:'innerHTML'}).then(()=>history.replaceState(history.state,'','/tasks?project_id=first&category=backlog#board'));'first tab'`)
		waitFor("first project workspace", `String(location.search==='?project_id=first&category=backlog' && !!document.getElementById('settings-workspace'))`, "true")
		b.evaluate(`window.workspaceNavigation.navigate('/themes?project_id=first',true);'first settings'`)
		waitFor("first project settings", `String(location.search==='?project_id=first' && !!document.getElementById('settings-retained-workspace'))`, "true")
		b.evaluate(`htmx.ajax('GET','/tasks?project_id=second',{target:'#main-content',swap:'innerHTML'}).then(()=>history.replaceState(history.state,'','/tasks?project_id=second'));'second tab'`)
		waitFor("second project workspace", `String(location.search==='?project_id=second' && !document.getElementById('themes-container') && !document.getElementById('settings-retained-workspace'))`, "true")
		b.evaluate(`window.workspaceNavigation.navigate('/themes?project_id=first',true);'return to first settings'`)
		waitFor("first settings return destination", `document.querySelector('#appearance-settings-nav [data-settings-back]').getAttribute('href')`, "/tasks?project_id=first&category=backlog#board")
		waitFor("second workspace is not retained by first settings", `String(!document.getElementById('settings-retained-workspace'))`, "true")
		b.evaluate(`document.querySelector('#appearance-settings-nav [data-settings-back]').click();'return to first workspace'`)
		waitFor("first workspace and sidebar restored", `String(location.search==='?project_id=first&category=backlog' && location.hash==='#board' && !document.documentElement.classList.contains('appearance-settings') && document.getElementById('task-list-groups').getAttribute('hx-get')==='/tasks/sidebar?project_id=first')`, "true")

		// Browser Back updates location before Settings loads. Ownership must
		// still describe the second project's mounted workspace at that point.
		b.evaluate(`window.workspaceNavigation.navigate('/themes?project_id=first',true);'settings before history navigation'`)
		waitFor("settings before switching projects", `String(location.search==='?project_id=first' && !!document.getElementById('themes-container'))`, "true")
		b.evaluate(`var source=document.createElement('a');source.setAttribute('hx-push-url','true');document.body.appendChild(source);htmx.ajax('GET','/tasks?project_id=second',{source:source,target:'#main-content',swap:'innerHTML'}).finally(()=>source.remove());'switch project with history'`)
		waitFor("second project before browser back", `String(location.search==='?project_id=second' && !!document.getElementById('settings-workspace') && !document.getElementById('themes-container'))`, "true")
		b.evaluate(`window.secondProjectWorkspace=document.getElementById('settings-workspace');history.back();'browser back to first settings'`)
		waitFor("first settings restored by history", `String(location.search==='?project_id=first' && !!document.getElementById('themes-container'))`, "true")
		waitFor("history does not retain second project workspace", `String(!document.getElementById('settings-retained-workspace') && !window.secondProjectWorkspace.isConnected)`, "true")
		waitFor("history preserves first project return destination", `document.querySelector('#appearance-settings-nav [data-settings-back]').getAttribute('href')`, "/tasks?project_id=first&category=backlog#board")
		b.evaluate(`document.querySelector('#appearance-settings-nav [data-settings-back]').click();'return after browser back'`)
		waitFor("history return restores first workspace and sidebar", `String(location.search==='?project_id=first&category=backlog' && location.hash==='#board' && !!document.getElementById('settings-workspace') && document.getElementById('settings-workspace')!==window.secondProjectWorkspace && !document.documentElement.classList.contains('appearance-settings') && document.getElementById('task-list-groups').getAttribute('hx-get')==='/tasks/sidebar?project_id=first')`, "true")

		// Revisit both threads so project switching uses the synchronous cached
		// preview before the network response commits its URL.
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1280, "height": 900, "deviceScaleFactor": 1, "mobile": false}, nil)
		for _, thread := range []string{"first", "second", "first"} {
			url := "/tasks/" + thread + "-thread?project_id=" + thread
			b.evaluate(`window.openVibelyNavigate('` + url + `');'open thread'`)
			waitFor("thread ready", `String(location.pathname+location.search==='`+url+`' && !!document.getElementById('task-thread-view'))`, "true")
		}
		b.evaluate(`window.workspaceNavigation.navigate('/themes?project_id=first',true);'settings from thread'`)
		waitFor("thread retained by settings", `String(location.pathname==='/themes' && !!document.getElementById('settings-retained-workspace'))`, "true")
		b.evaluate(`window.cachedProjectPreview=false;document.addEventListener('htmx:afterSwap',function(event){if(event.detail._ovCachedPreview)window.cachedProjectPreview=true;});window.openVibelyNavigate('/tasks/second-thread?project_id=second');'second thread tab'`)
		waitFor("second thread tab ready", `String(location.pathname==='/tasks/second-thread' && !document.getElementById('themes-container'))`, "true")
		waitFor("cached project preview used", `String(window.cachedProjectPreview)`, "true")
		b.evaluate(`window.openVibelyNavigate('/themes?project_id=first');'first settings tab'`)
		waitFor("settings tab restored", `String(location.pathname==='/themes' && !!document.getElementById('themes-container'))`, "true")
		waitFor("original thread return destination", `document.querySelector('#appearance-settings-nav [data-settings-back]').getAttribute('href')`, "/tasks/first-thread?project_id=first")
		b.evaluate(`document.querySelector('#appearance-settings-nav [data-settings-back]').click();'back to original thread'`)
		waitFor("original thread restored", `String(location.pathname==='/tasks/first-thread' && location.search==='?project_id=first' && !!document.getElementById('task-thread-view') && !document.documentElement.classList.contains('appearance-settings'))`, "true")

		// Search on a direct Settings link does not need a workspace request.
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1280, "height": 900, "deviceScaleFactor": 1, "mobile": false}, nil)
		failWorkspace.Store(true)
		b.call("Page.navigate", map[string]any{"url": server.URL + "/themes"}, nil)
		waitFor("direct settings ready", `String(!!window.syncAppearanceSettings && !!document.getElementById('themes-container') && !document.getElementById('settings-retained-workspace'))`, "true")
		before := workspaceRequests.Load()
		b.evaluate(`document.getElementById('task-list-search-toggle').click();'search direct settings'`)
		waitFor("direct settings search focused", `String(document.activeElement===document.getElementById('task-list-search-input') && document.documentElement.classList.contains('appearance-settings'))`, "true")
		if n := workspaceRequests.Load() - before; n != 0 {
			t.Fatalf("settings Search requested workspace %d times", n)
		}
	})
}

// Outside clicks dismiss the shared picker before navigation handlers run.
// A second click navigates, and retained composers remain usable.
func TestBrowserFunctional_SettingsClosesConversationModelPicker(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		var content templ.Component
		switch r.URL.Path {
		case "/themes":
			content = ThemesContent(nil)
		case "/chat", "/tasks/picker-thread":
			content = components.ChatInputForm(components.ChatInputFormConfig{
				FormID: "picker-form", InputID: "picker-message", TargetID: "messages",
				PostEndpoint: "/chat/send", ProjectID: "project", ShowModelSelector: true, ShowModeSelector: true,
				Agents:          []models.LLMConfig{{ID: "model", Name: "Test model", Provider: models.ProviderOpenAI, Model: "test"}},
				SelectedAgentID: "model",
			})
		default:
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Header.Get("HX-Request") == "true" {
			if err := content.Render(r.Context(), w); err != nil {
				t.Error(err)
			}
			return
		}
		if err := layout.Base("Picker navigation", []models.Project{{ID: "project", Name: "Project"}}, "project").Render(templ.WithChildren(r.Context(), content), w); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	for _, route := range []string{"/chat", "/tasks/picker-thread"} {
		t.Run(route, func(t *testing.T) {
			runComposerFocusCDP(t, chrome, server.URL+route, "settings-picker", func(b *composerFocusCDP) {
				b.waitFor("picker ready", `String(document.readyState==='complete' && !!window.ovModelPicker && !!window.workspaceNavigation && !!document.querySelector('.chat-model-select'))`, "true")
				b.evaluate(`window.originalPickerButton=document.querySelector('.chat-model-select');window.ovModelPicker.open(window.originalPickerButton);'opened'`)
				b.waitFor("picker visible", `String(!document.getElementById('conversation-model-picker').hidden)`, "true")
				b.click(`#conversation-model-picker .ov-mp-provider`)
				b.waitFor("submenu visible", `String(!document.getElementById('conversation-provider-models').hidden)`, "true")
				b.evaluate(`window.pickerNavigationRequests=0;document.addEventListener('htmx:beforeRequest',()=>window.pickerNavigationRequests++);window.pickerOriginalURL=location.href;'watch navigation'`)
				b.clickMenuBackdrop(`#sidebar [data-nav-base="/themes"]`)
				b.waitFor("first click only dismisses", `String(document.getElementById('conversation-model-picker').hidden && document.getElementById('conversation-provider-models').hidden && window.originalPickerButton.getAttribute('aria-expanded')==='false' && location.href===window.pickerOriginalURL && !document.getElementById('settings-retained-workspace') && window.pickerNavigationRequests===0 && !window._sidebarNavigating)`, "true")
				b.click(`#sidebar [data-nav-base="/themes"]`)
				b.waitFor("workspace hidden in settings", `String(!!document.getElementById('themes-container') && !!window.originalPickerButton.closest('#settings-retained-workspace'))`, "true")
				b.waitFor("picker and submenu dismissed", `String(document.getElementById('conversation-model-picker').hidden && document.getElementById('conversation-provider-models').hidden && window.originalPickerButton.getAttribute('aria-expanded')==='false')`, "true")
				b.evaluate(`window.restoreSettingsWorkspace(false);window.ovModelPicker.open(window.originalPickerButton);'reopened'`)
				b.waitFor("retained picker usable", `String(window.originalPickerButton.isConnected && !document.getElementById('conversation-model-picker').hidden)`, "true")
				// Every shared menu consumes dismissal before Settings' capture handler.
				windowClose := `window.ovModelPicker.close(false);`
				for _, menu := range []struct{ name, open, closed string }{
					{"mode", `window._openChatCustomSelectPanel(document.querySelector('.chat-mode-select'))`, `document.getElementById('chat-custom-select-panel').style.display==='none'`},
					{"tab", `document.querySelector('[data-project-tab="project"]').dispatchEvent(new KeyboardEvent('keydown',{key:'ContextMenu',bubbles:true,cancelable:true}))`, `!document.getElementById('project-tab-menu').matches(':popover-open')`},
					{"generic", `document.body.insertAdjacentHTML('beforeend','<div id="dismiss-test" data-menu-dropdown class="dropdown dropdown-open" style="position:fixed;left:300px;top:200px"><label tabindex="0">Actions</label><ul data-menu-content class="dropdown-content menu"><li><button>Action</button></li></ul></div>')`, `document.getElementById('dismiss-test').hasAttribute('data-menu-dismissed')`},
					{"task context", `document.querySelector('[data-task-list-context-menu]').setAttribute('data-kanban-menu-open','true')`, `document.querySelector('[data-task-list-context-menu]').getAttribute('data-kanban-menu-open')!=='true'`},
				} {
					b.evaluate(windowClose + menu.open + `;'opened'`)
					b.waitFor(menu.name+" open", `String(!(`+menu.closed+`))`, "true")
					b.evaluate(`window.pickerNavigationRequests=0;window.pickerOriginalURL=location.href;'reset'`)
					b.clickMenuBackdrop(`#sidebar [data-nav-base="/themes"]`)
					b.waitFor(menu.name+" first click only dismisses", `String((`+menu.closed+`) && location.href===window.pickerOriginalURL && window.pickerNavigationRequests===0 && !document.getElementById('settings-retained-workspace'))`, "true")
					b.click(`#sidebar [data-nav-base="/themes"]`)
					b.waitFor(menu.name+" second click navigates", `String(!!document.getElementById('settings-retained-workspace'))`, "true")
					b.evaluate(`window.restoreSettingsWorkspace(false);document.getElementById('dismiss-test')?.remove();'restored'`)
				}

				// The same lifecycle must work without Settings or HTMX events.
				for _, hide := range []string{"owner.hidden=true", "owner.inert=true", "owner.style.display='none'", "owner.remove()"} {
					b.evaluate(`window.pickerOwner=window.originalPickerButton.closest('form');window.pickerParent=window.pickerOwner.parentElement;window.ovModelPicker.open(window.originalPickerButton);'opened'`)
					b.waitFor("generic picker open", `String(!document.getElementById('conversation-model-picker').hidden)`, "true")
					b.evaluate(`(function(){const owner=window.pickerOwner;` + hide + `;return 'hidden';})()`)
					b.waitFor("unavailable owner dismisses picker and backdrop", `String(document.getElementById('conversation-model-picker').hidden && window.originalPickerButton.getAttribute('aria-expanded')==='false' && !document.querySelector('#ov-menu-backdrop:popover-open'))`, "true")
					b.evaluate(`window.ovModelPicker.open(window.originalPickerButton);'attempt reopen'`)
					b.waitFor("hidden owner cannot reopen picker", `String(document.getElementById('conversation-model-picker').hidden)`, "true")
					b.evaluate(`window.pickerOwner.hidden=false;window.pickerOwner.inert=false;window.pickerOwner.style.display='';if(!window.pickerOwner.isConnected)window.pickerParent.append(window.pickerOwner);'restored'`)
				}

			})
		})
	}
}

// Exercise real pointer input: neither document navigation handlers nor the
// underlying control should receive the dismissal gesture.
func assertMenuConsumesOutsideClick(b *composerFocusCDP, closed string) {
	b.evaluate(`window.menuOutsidePresses=0;window.menuOutsideClicks=0;window.menuOutsideAbort=new AbortController();document.addEventListener('pointerdown',()=>window.menuOutsidePresses++,{capture:true,signal:window.menuOutsideAbort.signal});document.addEventListener('click',()=>window.menuOutsideClicks++,{capture:true,signal:window.menuOutsideAbort.signal});var probe=document.createElement('button');probe.id='menu-outside-probe';probe.type='button';probe.textContent='Outside';probe.style='position:fixed;top:4px;left:4px;z-index:2147483647;cursor:pointer';(document.querySelector('dialog:modal')||document.body).append(probe);'ready'`)
	b.clickMenuBackdrop(`#menu-outside-probe`)
	b.waitFor("first outside click only dismisses", `String((`+closed+`) && window.menuOutsidePresses===0 && window.menuOutsideClicks===0)`, "true")
	b.click(`#menu-outside-probe`)
	b.waitFor("second outside click acts normally", `String(window.menuOutsidePresses===1 && window.menuOutsideClicks===1 && document.getElementById("menu-outside-probe").matches(":hover") && getComputedStyle(document.getElementById("menu-outside-probe")).cursor==="pointer")`, "true")
	b.evaluate(`window.menuOutsideAbort.abort();document.getElementById('menu-outside-probe').remove();'removed'`)
}

func TestBrowserFunctional_ChatProjectPickerConsumesOutsideClick(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		requests := []*events.ChatInputRequestEvent{{ID: "projects", ProjectID: "project", Questions: []events.ChatInputRequestQuestion{{ID: "scope", Kind: "projects", Question: "Choose projects", Options: []events.ChatInputRequestOption{{Label: "Specific projects"}}, Projects: []events.ChatInputProject{{ID: "project", Name: "Project"}}}}}}
		if r.URL.Path == "/chat/input-requests" {
			if err := ChatInputRequestCards(requests).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
			return
		}
		if r.URL.Path != "/chat" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := Chat([]models.Project{{ID: "project", Name: "Project"}}, "project", nil, nil, nil, nil, requests, false, false, 20).Render(r.Context(), w); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/chat", "chat-project-dismissal", func(b *composerFocusCDP) {
		b.waitFor("chat initialized", `String(!!window._chatInputRequestClickBound && !!document.querySelector('[data-project-picker]'))`, "true")
		b.click(`[data-option-label="Specific projects"]`)
		b.click(`[data-project-search]`)
		b.waitFor("project results visible", `String(document.querySelector('[data-project-results]').checkVisibility())`, "true")
		b.evaluate(`window.projectOutsideClicks=0;var probe=document.createElement('button');probe.id='project-outside-probe';probe.textContent='Outside';probe.style='position:fixed;top:4px;left:4px;z-index:2147483647';probe.onclick=()=>window.projectOutsideClicks++;document.body.append(probe);'ready'`)
		b.clickMenuBackdrop(`#project-outside-probe`)
		b.waitFor("single outside click hides results without moving focus", `String(document.querySelector('[data-project-results]').classList.contains('hidden') && document.activeElement.matches('[data-project-search]') && window.projectOutsideClicks===0)`, "true")
		b.click(`[data-project-search]`)
		b.waitFor("still-focused search reopens results", `String(document.querySelector('[data-project-results]').checkVisibility())`, "true")
		b.evaluate(`document.getElementById('project-outside-probe').remove();'removed'`)
		assertMenuConsumesOutsideClick(b, `document.querySelector('[data-project-results]').classList.contains('hidden')`)
		b.click(`[data-project-search]`)
		b.click(`[data-project-choice="project"]`)
		b.waitFor("project selection still works", `document.querySelector('[data-project-choice="project"]').getAttribute('aria-pressed')`, "true")
		for _, target := range []string{"[data-project-search]", "[data-project-choice]", "[data-project-remove]"} {
			b.click(`[data-project-search]`)
			b.evaluate(`document.querySelector('` + target + `').focus();'focused'`)
			b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Escape", "code": "Escape", "windowsVirtualKeyCode": 27}, nil)
			b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "Escape", "code": "Escape", "windowsVirtualKeyCode": 27}, nil)
			b.waitFor("Escape releases picker and preserves selection from "+target, `String(!document.querySelector('[data-project-results]').checkVisibility() && !document.querySelector('#ov-menu-backdrop:popover-open') && document.activeElement.matches('[data-project-search]') && document.querySelector('[data-project-choice="project"]').getAttribute('aria-pressed')==='true')`, "true")
		}
		// Real Tab navigation must reach Submit after Escape, without reopening
		// the results or requiring an outside pointer click.
		for i := 0; i < 5; i++ {
			if b.evaluate(`String(document.activeElement.matches('[data-chat-input-nav="submit"]'))`) == "true" {
				break
			}
			b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Tab", "code": "Tab", "windowsVirtualKeyCode": 9}, nil)
			b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "Tab", "code": "Tab", "windowsVirtualKeyCode": 9}, nil)
		}
		b.waitFor("keyboard reaches Submit after dismissal", `String(document.activeElement.matches('[data-chat-input-nav="submit"]') && !document.activeElement.disabled && !document.querySelector('[data-project-results]').checkVisibility())`, "true")
		b.click(`[data-project-search]`)
		b.waitFor("project search reopens after Escape", `String(document.querySelector('[data-project-results]').checkVisibility())`, "true")
	})
}

func TestBrowserFunctional_UnmarkedDropdownsConsumeOutsideClick(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		var content templ.Component
		switch r.URL.Path {
		case "/fixture/skills":
			content = SkillsContent(nil, true)
		case "/fixture/channels":
			content = SettingsContent(ChannelsSettingsView{})
		case "/fixture/automations":
			content = AutomationsContent(nil, "project")
		case "/fixture/chat":
			content = ChatContent(nil, nil, "project", nil, nil, nil, false, false, 20)
		case "/fixture/merge":
			content = WorktreeInfoPanel(&models.Task{ID: "task", WorktreeBranch: "feature", MergeTargetBranch: "main"}, nil)
		default:
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := layout.Base("Dropdowns", []models.Project{{ID: "project", Name: "Project"}}, "project").Render(templ.WithChildren(r.Context(), content), w); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	for _, tc := range []struct{ name, route, setup, trigger, panel string }{
		{"skills", "skills", "", `[aria-label="Add Skill"]`, `[aria-label="Add Skill"] + ul`},
		{"channels", "channels", "", `[aria-label="Add Channel"]`, `[aria-label="Add Channel"] + ul`},
		{"automations", "automations", "", `[aria-label="New Automation"]`, `[data-automation-new-menu]`},
		{"chat", "chat", "", `[aria-controls="chat-actions-menu"]`, `#chat-actions-menu`},
		{"filters", "skills", "", `[data-card-filters-button]`, `[data-card-filters-popover]`},
		{"merge", "merge", `document.querySelector('#worktree-info-panel details').open=true`, `#merge-dropdown > [role="button"]`, `#merge-dropdown > ul`},
		{"user", "skills", `document.getElementById('sidebar-auth-user').style.setProperty('display','flex','important')`, `#sidebar-user-menu-trigger`, `#sidebar-user-menu`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runComposerFocusCDP(t, chrome, server.URL+"/fixture/"+tc.route, "dropdown-"+tc.name, func(b *composerFocusCDP) {
				b.waitFor("page ready", `String(document.readyState==='complete' && typeof handleDropdownToggle==='function')`, "true")
				if tc.setup != "" {
					b.evaluate(tc.setup + `;'setup'`)
				}
				visible := `(()=>{const panel=document.querySelector('` + tc.panel + `');return panel.getClientRects().length>0 && getComputedStyle(panel).visibility==='visible' && getComputedStyle(panel).opacity!=='0'})()`
				b.click(tc.trigger)
				b.waitFor("menu open", `String(`+visible+`)`, "true")
				assertMenuConsumesOutsideClick(b, `!`+visible)
				b.click(tc.trigger)
				b.waitFor("menu reopens", `String(`+visible+`)`, "true")
				assertMenuConsumesOutsideClick(b, `!`+visible)
				b.click(tc.trigger)
				b.waitFor("menu opens for keyboard dismissal", `String(`+visible+`)`, "true")
				b.evaluate(`document.querySelector('` + tc.panel + `').querySelector('button, input, select, a[href], [tabindex="0"]').focus();'focused item'`)
				b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Escape"}, nil)
				b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "Escape"}, nil)
				b.waitFor("Escape closes menu and restores trigger focus", `String(!`+visible+` && document.activeElement===document.querySelector('`+tc.trigger+`'))`, "true")
				b.click(tc.trigger)
				b.waitFor("menu reopens after Escape", `String(`+visible+`)`, "true")

			})
		})
	}
}

func TestBrowserFunctional_AutomationViewSelectionRestoresMenuFocus(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	graph := models.AutomationLiveGraph{Automation: models.Automation{ID: "focus", Name: "Focus", LifecycleState: models.AutomationActive}, Version: models.AutomationVersion{ID: "saved"}, YAML: "schema_version: 1\nname: Focus\n"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path != "/fixture" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := layout.Base("Selection focus", nil, "project").Render(templ.WithChildren(r.Context(), AutomationLiveContent(graph, "project", true)), w); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/fixture", "automation-selection-focus", func(b *composerFocusCDP) {
		b.waitFor("ready", `String(document.readyState==='complete' && !!document.querySelector('[data-automation-view-yaml]'))`, "true")
		for _, view := range []string{"yaml", "graph", "yaml"} {
			b.click(`[data-automation-live-menu] > label`)
			b.waitFor("menu reopened", `String(getComputedStyle(document.querySelector('[data-automation-live-menu] > ul')).visibility==='visible')`, "true")
			b.click(`[data-automation-view-` + view + `]`)
			b.waitFor("view selected", `document.querySelector('[data-automation-view-`+view+`]').getAttribute('aria-pressed')`, "true")
			b.waitFor("selection closes menu and restores focus", `String(document.activeElement===document.querySelector('[data-automation-live-menu] > label') && getComputedStyle(document.querySelector('[data-automation-live-menu] > ul')).visibility==='hidden')`, "true")
		}
	})
}
