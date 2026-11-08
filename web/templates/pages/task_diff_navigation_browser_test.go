package pages

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
)

// Exercise the real cached page navigation with the summary arriving both before
// and after the lazy composer and the authoritative replacement page.
func TestBrowserFunctional_TaskDiffControlSurvivesNavigation(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "diff-nav-project", Name: "Diff navigation"}
	var summaryDelay, threadDelay atomic.Int64
	var revision atomic.Int64
	events := make(chan string, 1)
	render := func(c templ.Component) string {
		var out bytes.Buffer
		if err := c.Render(context.Background(), &out); err != nil {
			t.Error(err)
		}
		return out.String()
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/events/ws" {
			serveBrowserLiveFixture(w, r, events)
			return
		}
		if r.URL.Path == "/other" {
			content := templ.Raw(`<div id="other-page">Another app page</div>`)
			fmt.Fprint(w, render(content))
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/tasks/")
		id := strings.Split(path, "/")[0]
		if id != "diff-nav-a" && id != "diff-nav-b" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		task := &models.Task{ID: id, ProjectID: project.ID, Title: id, Status: models.StatusCompleted, Category: models.CategoryCompleted}
		if path == id {
			revision.Add(1)
		}
		thread := func() templ.Component {
			inputs := []models.ThreadInput{
				{ID: "queued-" + id, TaskID: id, InputMode: models.ThreadInputModeQueued, Content: fmt.Sprintf("Queued revision %d", revision.Load())},
				{ID: "steered-" + id, TaskID: id, InputMode: models.ThreadInputModeSteering, Content: fmt.Sprintf("Steered revision %d", revision.Load())},
			}
			return components.TaskThreadView(task, nil, nil, nil, nil, inputs, false, 30)
		}
		switch {
		case strings.HasSuffix(path, "/changes/summary"):
			time.Sleep(time.Duration(summaryDelay.Load()) * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"files":3,"insertions":7,"deletions":2,"review_comments":0}`)
		case strings.HasSuffix(path, "/thread"):
			time.Sleep(time.Duration(threadDelay.Load()) * time.Millisecond)
			fmt.Fprint(w, render(thread()))
		case path == id:
			if r.Header.Get("HX-Request") == "true" {
				time.Sleep(250 * time.Millisecond)
				if r.Header.Get("X-OpenVibely-Thread-Refresh") == "true" {
					fmt.Fprint(w, render(TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "chat", nil, thread())))
				} else {
					fmt.Fprint(w, render(TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "chat", nil)))
				}
			} else {
				fmt.Fprint(w, render(TaskDetailPage([]models.Project{project}, task, nil, nil, nil, nil, nil, nil, "chat", nil)))
			}
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/tasks/diff-nav-a?tab=chat", "diff-navigation", func(b *composerFocusCDP) {
		visible := `String(!!document.getElementById('task-change-activity') && document.getElementById('task-change-activity').checkVisibility())`
		b.waitFor("initial diff control", visible, "true")
		b.click("#task-message-input")
		b.typeText("Keep my unsent draft")
		// Capture the cached composer before the authoritative response arrives.
		b.evaluate(`document.addEventListener('htmx:afterSwap', function(event) {
			if (event.detail._ovCachedPreview) {
				window.cachedComposer = document.getElementById('task-thread-form');
				window.cachedDiffControl = document.getElementById('task-change-activity');
			}
		}); 'ready'`)

		for _, timing := range []struct {
			name            string
			summary, thread int64
		}{{"summary-first", 0, 500}, {"composer-first", 700, 0}, {"similar-timing", 250, 250}} {
			summaryDelay.Store(timing.summary)
			threadDelay.Store(timing.thread)
			for _, dest := range []string{"/tasks/diff-nav-b?tab=chat", "/tasks/diff-nav-a?tab=chat", "/other", "/tasks/diff-nav-a?tab=chat", "/tasks/diff-nav-b?tab=chat", "/tasks/diff-nav-a?tab=chat"} {
				b.evaluate(`window.cachedComposer = null; window.cachedDiffControl = null; 'reset'`)
				result := b.evaluateAwait(fmt.Sprintf(`window.openVibelyNavigate(%q).then(function(){return 'navigated'})`, dest))
				if result != "navigated" {
					t.Fatal(result)
				}
				if dest == "/other" {
					b.waitFor("other page", `String(!!document.getElementById('other-page'))`, "true")
					continue
				}
				b.waitForTimeout(timing.name+" "+dest, visible, "true", 3*time.Second)
				b.waitFor("one diff control inside composer", `String(document.querySelectorAll('#task-change-activity').length === 1 && !!document.querySelector('#task-thread-form > #task-change-activity'))`, "true")
				b.waitFor("fresh queued and steered rows", fmt.Sprintf(`String(document.querySelector('#pending-thread-inputs [data-thread-input-id="queued-' + document.getElementById('task-detail-content').dataset.taskId + '"]').textContent.includes('Queued revision %d') && document.querySelector('#pending-thread-inputs [data-thread-input-id="steered-' + document.getElementById('task-detail-content').dataset.taskId + '"]').textContent.includes('Steered revision %d'))`, revision.Load(), revision.Load()), "true")
				if strings.Contains(dest, "diff-nav-a") {
					b.waitFor("draft retained", `document.getElementById('task-message-input').value`, "Keep my unsent draft")
					b.waitFor("cached composer and diff control retained together", `String(!!window.cachedComposer && window.cachedComposer === document.getElementById('task-thread-form') && window.cachedDiffControl === document.getElementById('task-change-activity'))`, "true")
				}
				t.Logf("%s %s: diff control visible; queued/steered rows refreshed", timing.name, dest)
			}
		}
		// A normal thread morph must still preserve the existing diff control.
		b.evaluate(`window.beforeMorphDiff = document.getElementById('task-change-activity'); document.getElementById('task-thread-view').setAttribute('data-thread-revision', 'force-morph'); 'ready'`)
		b.evaluateAwait(`htmx.ajax('GET', '/tasks/diff-nav-a/thread', {target:'#task-thread-view',swap:'morph:outerHTML'}).then(function(){return 'morphed'})`)
		t.Logf("after morph: %s", b.evaluate(`JSON.stringify({same:window.beforeMorphDiff === document.getElementById('task-change-activity'),visible:document.getElementById('task-change-activity')?.checkVisibility(),preserve:document.getElementById('task-change-activity')?.getAttribute('hx-preserve')})`))
		b.waitFor("diff preserved during thread morph", `String(window.beforeMorphDiff === document.getElementById('task-change-activity') && window.beforeMorphDiff.checkVisibility())`, "true")
		b.waitFor("draft retained after morph", `document.getElementById('task-message-input').value`, "Keep my unsent draft")
	})
}
