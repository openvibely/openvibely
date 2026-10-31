package pages

import (
	"fmt"
	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/web/templates/layout"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
)

func TestBrowserFunctional_ChangesExplorer(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "explorer-project", Name: "Explorer"}
	task := &models.Task{ID: "explorer-task", ProjectID: project.ID, Title: "Explore changes", Status: models.StatusCompleted, Category: models.CategoryCompleted}
	diff := ""
	for _, path := range []string{"src/first.go", "src/nested/second.go"} {
		diff += fmt.Sprintf("diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1,160 +1,160 @@\n", path, path, path, path)
		for line := 0; line < 160; line++ {
			diff += fmt.Sprintf("-old %d\n+%s line %d\n", line, path, line)
		}
	}
	var cleared atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/tasks":
			content := templ.Raw(`<a id="open-explorer-task" href="/tasks/explorer-task" hx-get="/tasks/explorer-task" hx-target="#main-content" hx-push-url="true">Task</a>`)
			if err := layout.Base("Tasks", []models.Project{project}, project.ID).Render(templ.WithChildren(r.Context(), content), w); err != nil {
				t.Error(err)
			}
		case "/models":
			fmt.Fprint(w, `<div id="models-container">Models</div>`)
		case "/themes":
			if err := ThemesContent(nil).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		case "/tasks/explorer-task":
			if r.Header.Get("HX-Request") == "true" {
				tab := r.URL.Query().Get("tab")
				if tab == "" {
					tab = "chat"
				}
				if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, tab, nil).Render(r.Context(), w); err != nil {
					t.Error(err)
				}
				return
			}
			err := TaskDetailPage([]models.Project{project}, task, nil, nil, nil, nil, nil, nil, "chat", nil).Render(r.Context(), w)
			if err != nil {
				t.Error(err)
			}
		case "/tasks/explorer-task/thread":
			fmt.Fprint(w, `<textarea id="task-message-input">Keep this draft</textarea>`)
		case "/tasks/explorer-task/changes":
			currentDiff := diff
			if cleared.Load() {
				currentDiff = ""
			}
			if err := TaskChangesContent(currentDiff, task.ID, nil).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		case "/tasks/explorer-task/changes/file":
			index, _ := strconv.Atoi(r.URL.Query().Get("file_index"))
			if err := components.LoadDiffFileCard(diff, index, r.URL.Query().Get("view"), task.ID, nil, true).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		case "/tasks/explorer-task/changes/summary":
			fmt.Fprint(w, `{"files":2,"insertions":2,"deletions":2}`)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/tasks?project_id="+project.ID, "changes-explorer", func(b *composerFocusCDP) {
		b.waitFor("board ready", `String(document.readyState==='complete' && !!window.htmx)`, "true")
		b.evaluate(`document.getElementById('open-explorer-task').click();'task'`)
		b.waitFor("workspace ready", `String(document.readyState==='complete' && !!window.taskWorkspace && !!document.getElementById('task-message-input'))`, "true")
		b.evaluate(`document.getElementById('task-list-toggle').click();window.taskWorkspace.openChanges();'open'`)
		b.waitFor("tree replaces collapsed task list", `String(document.documentElement.classList.contains('changes-explorer-active') && !document.documentElement.classList.contains('task-list-collapsed') && document.querySelectorAll('#changes-explorer-host [data-explorer-path]').length===2)`, "true")
		b.waitFor("automatically expanded toggle state", `JSON.stringify([document.getElementById('task-list-toggle').getAttribute('aria-expanded'),document.getElementById('task-list-toggle').getAttribute('aria-label')])`, `["true","Collapse task list"]`)
		b.waitFor("explorer controls visible in sidebar", `String(document.querySelector('#changes-explorer-host .explorer-back').getBoundingClientRect().height>0 && document.querySelector('#changes-explorer-host [data-explorer-path]').getBoundingClientRect().height>0)`, "true")
		b.evaluate(`document.getElementById('task-list-toggle').click();'collapse explorer'`)
		b.waitFor("explorer temporarily collapsed", `String(document.documentElement.classList.contains('task-list-collapsed'))`, "true")
		b.evaluate(`document.getElementById('task-list-toggle').click();'expand explorer'`)
		b.waitFor("explorer toggle preserves task preference", `JSON.stringify([document.documentElement.classList.contains('task-list-collapsed'),localStorage.getItem('openvibely.taskListCollapsed')])`, `[false,"true"]`)
		b.waitFor("titlebar controls remain visible", `String(['.task-list-brand','#task-list-search-toggle','#task-list-new'].every(function(selector){var el=document.querySelector(selector);return el.getBoundingClientRect().height>0 && getComputedStyle(el).visibility==='visible';}))`, "true")
		b.evaluate(`document.getElementById('task-list-search-toggle').click();'search tasks'`)
		b.waitFor("task search available from changes", `String(!document.documentElement.classList.contains('changes-explorer-active') && document.activeElement===document.getElementById('task-list-search-input'))`, "true")
		b.evaluate(`document.getElementById('task-list-search-input').value='Explore';document.getElementById('task-list-search-input').dispatchEvent(new Event('input'));window.taskWorkspace.openChanges();'changes with task search open'`)
		b.waitFor("changes active with prior search", `String(document.documentElement.classList.contains('changes-explorer-active'))`, "true")
		b.evaluate(`document.getElementById('task-list-search-toggle').click();'search again'`)
		b.waitFor("existing task search stays open and retains query", `String(!document.documentElement.classList.contains('changes-explorer-active') && document.activeElement===document.getElementById('task-list-search-input') && document.getElementById('task-list-search-input').getBoundingClientRect().height>0 && document.getElementById('task-list-search-input').value==='Explore')`, "true")
		b.evaluate(`document.getElementById('task-list-search-toggle').click();document.getElementById('task-list-toggle').click();window.taskWorkspace.openChanges();'return to changes'`)
		b.waitFor("all inline file cards remain visible", `String(!!document.querySelector('#diff-file-0 .diff-line-content') && ['diff-file-0','diff-file-1'].every(id=>document.getElementById(id).getBoundingClientRect().height>0))`, "true")
		b.evaluate(`toggleAllDiffFiles(false);document.querySelector('[data-explorer-path="src/nested/second.go"]').click();'expand and navigate'`)
		b.waitFor("second lazy diff loaded", `String(document.getElementById('diff-file-0').getBoundingClientRect().height>0 && document.getElementById('diff-file-1').getBoundingClientRect().height>0 && !!document.querySelector('#diff-file-1 .diff-line-content') && document.getElementById('diff-file-1').getBoundingClientRect().height>2000)`, "true")
		b.evaluate(`document.getElementById('tab-changes').scrollTop=900;'scroll'`)
		b.waitFor("long diff scrolled", `String(document.getElementById('tab-changes').scrollTop>500)`, "true")
		b.evaluate(`document.querySelector('[data-explorer-path="src/first.go"]').click();'select loaded file'`)
		b.waitFor("loaded file starts at header", `String(!document.getElementById('diff-file-0').hidden && Math.abs(document.getElementById('diff-file-0').getBoundingClientRect().top-document.getElementById('tab-changes').getBoundingClientRect().top)<2)`, "true")
		b.evaluate(`document.querySelector('[data-explorer-path="src/nested/second.go"]').click();'second'`)
		b.evaluate(`switchDiffView('split',false);'split'`)
		b.waitFor("split selection preserved", `String(['diff-file-split-0','diff-file-split-1'].every(id=>document.getElementById(id).getBoundingClientRect().height>0) && document.getElementById('diff-file-split-1').textContent.includes('second'))`, "true")
		b.evaluate(`document.querySelector('[data-explorer-path="src/nested/second.go"]').click();'navigate split file'`)
		b.waitFor("split file navigation reaches header without hiding other files", `String(Math.abs(document.getElementById('diff-file-split-1').getBoundingClientRect().top-document.getElementById('tab-changes').getBoundingClientRect().top)<2 && document.getElementById('diff-file-split-0').getBoundingClientRect().height>0)`, "true")
		b.evaluate(`var search=document.querySelector('.explorer-search');search.value='second';search.dispatchEvent(new Event('input'));'filter'`)
		b.waitFor("filtered nested tree", `String(document.querySelectorAll('[data-explorer-path]').length===1 && document.querySelectorAll('.changes-explorer details').length===2)`, "true")
		b.waitFor("filter only affects explorer", `String(['diff-file-split-0','diff-file-split-1'].every(id=>document.getElementById(id).getBoundingClientRect().height>0))`, "true")
		b.evaluate(`search.value='missing';search.dispatchEvent(new Event('input'));'empty'`)
		b.waitFor("empty search", `String(!document.querySelector('.explorer-empty').hidden && !document.querySelector('[data-explorer-path]'))`, "true")
		b.evaluate(`document.querySelector('.explorer-back').click();'back'`)
		b.waitFor("workspace and draft restored", `String(!document.documentElement.classList.contains('appearance-settings') && document.documentElement.classList.contains('task-list-collapsed') && document.getElementById('task-message-input').value==='Keep this draft')`, "true")
		b.evaluate(`window.taskWorkspace.openChanges();'reopen'`)
		b.waitFor("changes reopened", `String(!!document.querySelector('#tab-changes #diff-viewer') && !document.querySelector('#changes-content.htmx-settling'))`, "true")

		b.evaluate(`window.historyComposer=document.getElementById('task-message-input');window.historyComposer.value='Unsent history draft';history.back();'thread via browser back'`)
		b.waitFor("thread via history", `String(!document.documentElement.classList.contains('changes-explorer-active') && document.getElementById('task-message-input')===window.historyComposer)`, "true")
		b.evaluate(`history.forward();'changes via history'`)
		b.waitFor("changes via history", `String(document.documentElement.classList.contains('changes-explorer-active') && document.getElementById('task-message-input')===window.historyComposer && window.historyComposer.value==='Unsent history draft')`, "true")
		b.evaluate(`window.retainedTaskComposer=document.getElementById('task-message-input');window.retainedTaskWorkspace=window.taskWorkspace;document.getElementById('task-list-toggle').click();document.querySelector('.sidebar-aside [data-nav-base="/themes"]').click();'settings from collapsed explorer'`)
		b.waitFor("settings ready", `String(!!document.getElementById('themes-container') && !document.querySelector('#main-content.htmx-settling'))`, "true")
		b.waitFor("settings expands navigation without changing saved preference", `String(!document.documentElement.classList.contains('task-list-collapsed') && document.querySelector('#appearance-settings-nav [data-settings-back]').getBoundingClientRect().height>0 && localStorage.getItem('openvibely.taskListCollapsed')==='true')`, "true")

		b.evaluate(`window.settingsShortcutURL=location.href;window.settingsShortcutHistory=history.length;window.hiddenInspectorState=document.getElementById('task-details-panel').hidden;window.hiddenChangesState=document.getElementById('tab-changes').className;window.taskShortcutsIgnored=['KeyB','KeyD','ArrowUp','ArrowDown','KeyL'].every(code=>{var mac=/Mac|iPhone|iPad/.test(navigator.platform);var e=new KeyboardEvent('keydown',{code:code,key:code,metaKey:mac,ctrlKey:!mac,shiftKey:true,bubbles:true,cancelable:true});document.body.dispatchEvent(e);return !e.defaultPrevented;});'shortcuts in settings'`)
		b.waitFor("hidden task shortcuts ignored in settings", `String(window.taskShortcutsIgnored && location.href===window.settingsShortcutURL && history.length===window.settingsShortcutHistory && document.getElementById('task-details-panel').hidden===window.hiddenInspectorState && document.getElementById('tab-changes').className===window.hiddenChangesState)`, "true")

		b.evaluate(`document.querySelector('#appearance-settings-nav [data-nav-base="/models"]').click();'models'`)
		b.waitFor("models ready", `String(!!document.getElementById('models-container') && !document.querySelector('#settings-content.htmx-settling'))`, "true")
		b.evaluate(`history.back();'back within settings'`)
		b.waitFor("themes after back", `String(!!document.getElementById('themes-container') && !document.querySelector('#settings-content.htmx-settling'))`, "true")
		b.waitFor("changes task retained after settings history", `String(window.retainedTaskComposer.isConnected && document.getElementById('task-message-input')===window.retainedTaskComposer && window.retainedTaskComposer.value==='Unsent history draft')`, "true")
		b.waitFor("return URL retains task and changes view", `(function(){var u=new URL(document.querySelector('#appearance-settings-nav [data-settings-back]').href);return u.pathname+'?tab='+u.searchParams.get('tab');})()`, "/tasks/explorer-task?tab=changes")
		b.evaluate(`document.querySelector('#appearance-settings-nav [data-settings-back]').click();'return'`)
		b.waitFor("returned to changes", `String(location.pathname==='/tasks/explorer-task' && document.documentElement.classList.contains('changes-explorer-active') && !!document.querySelector('#tab-changes #diff-viewer') && !document.querySelector('#main-content.htmx-settling'))`, "true")
		b.waitFor("same task controller and composer after settings", `String(window.taskWorkspace===window.retainedTaskWorkspace && document.getElementById('task-message-input')===window.retainedTaskComposer)`, "true")
		b.evaluate(`document.querySelector('.explorer-back').click();'thread'`)
		b.waitFor("original collapse state after settings round trip", `String(document.documentElement.classList.contains('task-list-collapsed') && !document.documentElement.classList.contains('changes-explorer-active'))`, "true")
		b.waitFor("restored collapsed toggle state", `JSON.stringify([document.getElementById('task-list-toggle').getAttribute('aria-expanded'),document.getElementById('task-list-toggle').getAttribute('aria-label')])`, `["false","Expand task list"]`)
		b.evaluate(`document.getElementById('task-list-toggle').click();window.taskWorkspace.openChanges();'changes from expanded workspace'`)
		b.waitFor("changes opens from expanded workspace", `String(document.documentElement.classList.contains('changes-explorer-active') && !document.documentElement.classList.contains('task-list-collapsed'))`, "true")
		b.evaluate(`document.getElementById('task-list-toggle').click();window.syncAppearanceSettings();'temporarily collapse'`)
		b.waitFor("ordinary synchronization respects temporary collapse", `String(document.documentElement.classList.contains('task-list-collapsed') && localStorage.getItem('openvibely.taskListCollapsed')==='false')`, "true")
		b.evaluate(`window.taskWorkspace.openThread();'thread again'`)
		b.waitFor("expanded workspace is restored", `String(!document.documentElement.classList.contains('task-list-collapsed') && !document.documentElement.classList.contains('changes-explorer-active'))`, "true")
		b.waitFor("restored expanded toggle state", `JSON.stringify([document.getElementById('task-list-toggle').getAttribute('aria-expanded'),document.getElementById('task-list-toggle').getAttribute('aria-label')])`, `["true","Collapse task list"]`)
		b.evaluate(`window.taskWorkspace.openChanges();'changes'`)
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 390, "height": 844, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.waitFor("mobile explorer", `String(!!document.querySelector('#changes-explorer-mobile .changes-explorer') && document.getElementById('changes-explorer-mobile').getBoundingClientRect().height>0)`, "true")
		b.evaluate(`search.value='';search.dispatchEvent(new Event('input'));'clear filter'`)
		b.waitFor("files before refresh", `String(document.querySelectorAll('[data-explorer-path]').length===2)`, "true")
		cleared.Store(true)
		b.evaluate(`_updateDiffViewer('');'refresh empty'`)
		b.waitFor("live refresh clears explorer", `String(!document.querySelector('#tab-changes #diff-viewer') && document.querySelectorAll('[data-explorer-path]').length===0 && document.querySelector('.explorer-empty').textContent==='No changed files.')`, "true")
		cleared.Store(false)
		b.evaluate(`_updateDiffViewer('');'refresh files'`)
		b.waitFor("live refresh repopulates explorer", `String(document.querySelectorAll('[data-explorer-path]').length===2 && !document.getElementById('diff-file-0').hidden && !document.getElementById('diff-file-1').hidden)`, "true")
	})
}
