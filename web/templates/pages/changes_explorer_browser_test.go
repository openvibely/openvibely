package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
)

func TestBrowserFunctional_ChangesExplorer(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "explorer-project", Name: "Explorer"}
	task := &models.Task{ID: "explorer-task", ProjectID: project.ID, Title: "Explore changes", Status: models.StatusCompleted, Category: models.CategoryCompleted}
	diff := "diff --git a/src/first.go b/src/first.go\n--- a/src/first.go\n+++ b/src/first.go\n@@ -1 +1 @@\n-old\n+first\ndiff --git a/src/nested/second.go b/src/nested/second.go\n--- a/src/nested/second.go\n+++ b/src/nested/second.go\n@@ -1 +1 @@\n-old\n+second\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/tasks/explorer-task":
			err := TaskDetailPage([]models.Project{project}, task, nil, nil, nil, nil, nil, nil, "chat", nil).Render(r.Context(), w)
			if err != nil {
				t.Error(err)
			}
		case "/tasks/explorer-task/thread":
			fmt.Fprint(w, `<textarea id="task-message-input">Keep this draft</textarea>`)
		case "/tasks/explorer-task/changes":
			if err := TaskChangesContent(diff, task.ID, nil).Render(r.Context(), w); err != nil {
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
	runComposerFocusCDP(t, chrome, server.URL+"/tasks/explorer-task?project_id="+project.ID, "changes-explorer", func(b *composerFocusCDP) {
		b.waitFor("workspace ready", `String(document.readyState==='complete' && !!window.taskWorkspace && !!document.getElementById('task-message-input'))`, "true")
		b.evaluate(`document.getElementById('task-list-toggle').click();window.taskWorkspace.openChanges();'open'`)
		b.waitFor("tree replaces collapsed task list", `String(document.documentElement.classList.contains('changes-explorer-active') && !document.documentElement.classList.contains('task-list-collapsed') && document.querySelectorAll('#changes-explorer-host [data-explorer-path]').length===2)`, "true")
		b.waitFor("only first diff visible", `String(!!document.querySelector('#diff-file-0 .diff-line-content') && document.getElementById('diff-file-1').hidden)`, "true")
		b.evaluate(`document.querySelector('[data-explorer-path="src/nested/second.go"]').click();'select'`)
		b.waitFor("second lazy diff loaded", `String(document.getElementById('diff-file-0').hidden && !document.getElementById('diff-file-1').hidden && document.getElementById('diff-file-1').textContent.includes('second'))`, "true")
		b.evaluate(`switchDiffView('split',false);'split'`)
		b.waitFor("split selection preserved", `String(document.getElementById('diff-file-split-0').hidden && !document.getElementById('diff-file-split-1').hidden && document.getElementById('diff-file-split-1').textContent.includes('second'))`, "true")
		b.evaluate(`var search=document.querySelector('.explorer-search');search.value='second';search.dispatchEvent(new Event('input'));'filter'`)
		b.waitFor("filtered nested tree", `String(document.querySelectorAll('[data-explorer-path]').length===1 && document.querySelectorAll('.changes-explorer details').length===2)`, "true")
		b.evaluate(`search.value='missing';search.dispatchEvent(new Event('input'));'empty'`)
		b.waitFor("empty search", `String(!document.querySelector('.explorer-empty').hidden && !document.querySelector('[data-explorer-path]'))`, "true")
		b.evaluate(`document.querySelector('.explorer-back').click();'back'`)
		b.waitFor("workspace and draft restored", `String(!document.documentElement.classList.contains('appearance-settings') && document.documentElement.classList.contains('task-list-collapsed') && document.getElementById('task-message-input').value==='Keep this draft')`, "true")
		b.evaluate(`window.taskWorkspace.openChanges();'reopen'`)
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 390, "height": 844, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.waitFor("mobile explorer", `String(!!document.querySelector('#changes-explorer-mobile .changes-explorer') && document.getElementById('changes-explorer-mobile').getBoundingClientRect().height>0)`, "true")
	})
}
