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
		b.waitFor("only first diff visible", `String(!!document.querySelector('#diff-file-0 .diff-line-content') && document.getElementById('diff-file-1').hidden)`, "true")
		b.evaluate(`document.querySelector('[data-explorer-path="src/nested/second.go"]').click();'select'`)
		b.waitFor("second lazy diff loaded", `String(document.getElementById('diff-file-0').hidden && !document.getElementById('diff-file-1').hidden && !!document.querySelector('#diff-file-1 .diff-line-content') && document.getElementById('diff-file-1').getBoundingClientRect().height>2000)`, "true")
		b.evaluate(`document.getElementById('tab-changes').scrollTop=900;'scroll'`)
		b.waitFor("long diff scrolled", `String(document.getElementById('tab-changes').scrollTop>500)`, "true")
		b.evaluate(`document.querySelector('[data-explorer-path="src/first.go"]').click();'select loaded file'`)
		b.waitFor("loaded file starts at header", `String(!document.getElementById('diff-file-0').hidden && Math.abs(document.getElementById('diff-file-0').getBoundingClientRect().top-document.getElementById('tab-changes').getBoundingClientRect().top)<2)`, "true")
		b.evaluate(`document.querySelector('[data-explorer-path="src/nested/second.go"]').click();'second'`)
		b.evaluate(`switchDiffView('split',false);'split'`)
		b.waitFor("split selection preserved", `String(document.getElementById('diff-file-split-0').hidden && !document.getElementById('diff-file-split-1').hidden && document.getElementById('diff-file-split-1').textContent.includes('second'))`, "true")
		b.evaluate(`var search=document.querySelector('.explorer-search');search.value='second';search.dispatchEvent(new Event('input'));'filter'`)
		b.waitFor("filtered nested tree", `String(document.querySelectorAll('[data-explorer-path]').length===1 && document.querySelectorAll('.changes-explorer details').length===2)`, "true")
		b.evaluate(`search.value='missing';search.dispatchEvent(new Event('input'));'empty'`)
		b.waitFor("empty search", `String(!document.querySelector('.explorer-empty').hidden && !document.querySelector('[data-explorer-path]'))`, "true")
		b.evaluate(`document.querySelector('.explorer-back').click();'back'`)
		b.waitFor("workspace and draft restored", `String(!document.documentElement.classList.contains('appearance-settings') && document.documentElement.classList.contains('task-list-collapsed') && document.getElementById('task-message-input').value==='Keep this draft')`, "true")
		b.evaluate(`window.taskWorkspace.openChanges();'reopen'`)
		b.waitFor("changes reopened", `String(!!document.querySelector('#tab-changes #diff-viewer') && !document.querySelector('#changes-content.htmx-settling'))`, "true")
		b.evaluate(`document.querySelector('.sidebar-aside [data-nav-base="/themes"]').click();'settings'`)
		b.waitFor("settings ready", `String(!!document.getElementById('themes-container') && !document.querySelector('#main-content.htmx-settling'))`, "true")
		b.waitFor("return URL retains task and changes view", `(function(){var u=new URL(document.querySelector('#appearance-settings-nav [data-settings-back]').href);return u.pathname+'?tab='+u.searchParams.get('tab');})()`, "/tasks/explorer-task?tab=changes")
		b.evaluate(`document.querySelector('#appearance-settings-nav [data-settings-back]').click();'return'`)
		b.waitFor("returned to changes", `String(location.pathname==='/tasks/explorer-task' && document.documentElement.classList.contains('changes-explorer-active') && !!document.querySelector('#tab-changes #diff-viewer') && !document.querySelector('#main-content.htmx-settling'))`, "true")
		b.evaluate(`document.querySelector('.explorer-back').click();'thread'`)
		b.waitFor("original collapse state after settings round trip", `String(document.documentElement.classList.contains('task-list-collapsed') && !document.documentElement.classList.contains('changes-explorer-active'))`, "true")
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
		b.waitFor("live refresh repopulates explorer", `String(document.querySelectorAll('[data-explorer-path]').length===2 && !document.getElementById('diff-file-0').hidden && document.getElementById('diff-file-1').hidden)`, "true")
	})
}
