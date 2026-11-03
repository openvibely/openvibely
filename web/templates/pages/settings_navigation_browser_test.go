package pages

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
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
		for _, route := range []string{"models", "channels", "personality", "skills", "agents"} {
			b.evaluate(`document.querySelector('#appearance-settings-nav [data-nav-base="/` + route + `"] ').click();'configuration'`)
			waitFor("settings navigation survives "+route, `String(!!document.getElementById('`+route+`-container') && document.documentElement.classList.contains('appearance-settings') && !document.documentElement.classList.contains('task-list-collapsed'))`, "true")
			waitFor("settings rail selected", `String(document.querySelector('.sidebar-aside [data-nav-base="/themes"]').getAttribute('aria-current')==='page')`, "true")
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
