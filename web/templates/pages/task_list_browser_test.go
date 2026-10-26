package pages

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_TaskListGoalEventsAndSearch(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	var achieved atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/tasks/sidebar":
			task := models.Task{ID: "goal-task", ProjectID: "goal-project", Title: "Goal task", Status: models.StatusCompleted, Category: models.CategoryCompleted, GoalMet: achieved.Load()}
			if err := layout.TaskListPanelContent(task.ProjectID, layout.TaskListGroups([]models.Task{task})).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		case "/":
			if err := layout.Base("Tasks", []models.Project{{ID: "goal-project", Name: "Goal project"}}, "goal-project").Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "task-list-goal-events", func(b *composerFocusCDP) {
		state := `document.querySelector('[data-task-list-row="goal-task"] [data-task-state]')?.dataset.taskState || ''`
		b.waitFor("initial completed icon", state, "completed")
		// Search must reveal a collapsed group without saving the temporary expansion.
		b.evaluate(`document.querySelector('[data-task-list-group="Completed"]').open=false; document.getElementById('task-list-search-toggle').click(); var input=document.getElementById('task-list-search-input'); input.value='Goal task'; input.dispatchEvent(new Event('input')); 'searched'`)
		visible := `String(document.querySelector('[data-task-list-group="Completed"]').open && !document.querySelector('[data-task-list-row="goal-task"]').hidden)`
		b.waitFor("search opens matching group", visible, "true")
		achieved.Store(true)
		// Goal service events omit project_id; they must still refresh this list.
		b.evaluate(`window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'task_goal_evaluated',task_id:'goal-task',goal_status:'achieved'}})); 'sent'`)
		b.waitFor("goal achieved icon", state, "goal-met")
		achieved.Store(false)
		b.evaluate(`window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'task_goal_cleared',task_id:'goal-task'}})); 'sent'`)
		b.waitFor("cleared goal restores completed icon", state, "completed")
		b.waitFor("search survives live list replacements", visible, "true")
		b.evaluate(`document.getElementById('task-list-search-input').value=''; document.getElementById('task-list-search-input').dispatchEvent(new Event('input')); 'cleared'`)
		b.waitFor("clearing search restores collapsed group", `String(document.querySelector('[data-task-list-group="Completed"]').open)`, "false")
		b.waitFor("temporary expansion not persisted", `String(JSON.parse(localStorage.getItem('openvibely.taskListGroups')).Completed.closed)`, "true")
		b.evaluate(`document.querySelector('[data-task-list-group="Completed"]').open=true; var input=document.getElementById('task-list-search-input'); input.value='no match'; input.dispatchEvent(new Event('input')); input.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true})); 'closed search'`)
		b.waitFor("closing search preserves previously open group", `String(document.querySelector('[data-task-list-group="Completed"]').open && !document.querySelector('[data-task-list-group="Completed"]').hidden)`, "true")
	})
}

func TestBrowserFunctional_TaskListClickUsesDisplayedTask(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/tasks/sidebar":
			tasks := []models.Task{
				{ID: "task-a", Title: "Task A", Category: models.CategoryCompleted},
				{ID: "task-b", Title: "Task B", Category: models.CategoryCompleted},
			}
			if err := layout.TaskListPanelContent("project", layout.TaskListGroups(tasks)).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		case "/":
			if err := layout.Base("Tasks", []models.Project{{ID: "project", Name: "Project"}}, "project").Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "task-list-displayed-task", func(b *composerFocusCDP) {
		b.waitFor("task rows ready", `String(!!document.querySelector('[data-task-list-row="task-a"]'))`, "true")
		// A cached preview is already showing B while A remains in the address bar.
		b.evaluate(`htmx.process(document.getElementById('task-list-groups')); history.replaceState({},'', '/tasks/task-a?project_id=project'); document.getElementById('main-content').innerHTML='<div id="task-thread-view" data-task-id="task-b"></div>'; window.taskRowRequests=0; document.body.addEventListener('htmx:beforeRequest',e=>{if(e.detail.elt.matches('[data-task-list-row]')){window.taskRowRequests++;e.preventDefault();}}); document.querySelector('[data-task-list-row="task-a"]').click(); 'clicked'`)
		b.waitFor("click back to A starts navigation", `String(window.taskRowRequests)`, "1")
		b.evaluate(`document.getElementById('task-thread-view').dataset.taskId='task-a'; document.querySelector('[data-task-list-row="task-a"]').click(); 'clicked'`)
		b.waitFor("click on displayed task remains a no-op", `String(window.taskRowRequests)`, "1")
		// Cold navigation hides A behind the loading state; clicking A must cancel it too.
		b.evaluate(`document.getElementById('main-content').setAttribute('data-page-loading',''); document.querySelector('[data-task-list-row="task-a"]').click(); 'clicked'`)
		b.waitFor("click back during cold navigation is allowed", `String(window.taskRowRequests)`, "2")
	})
}

func TestBrowserFunctional_TaskListSearchFiltersExtraRows(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	tasks := []models.Task{}
	for _, title := range []string{"login-first", "other-2", "other-3", "other-4", "other-5", "other-6", "other-7", "login-extra", "unrelated-extra"} {
		tasks = append(tasks, models.Task{ID: title, Title: title, Category: models.CategoryCompleted})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/tasks/sidebar":
			if err := layout.TaskListPanelContent("project", layout.TaskListGroups(tasks)).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		case "/":
			if err := layout.Base("Tasks", []models.Project{{ID: "project", Name: "Project"}}, "project").Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "task-list-extra-search", func(b *composerFocusCDP) {
		visible := `JSON.stringify(Array.from(document.querySelectorAll('[data-task-list-row]')).filter(e=>getComputedStyle(e).display!=='none').map(e=>e.dataset.taskListRow))`
		b.waitFor("initial seven rows", visible, `["login-first","other-2","other-3","other-4","other-5","other-6","other-7"]`)
		b.evaluate(`document.getElementById('task-list-search-toggle').click(); var search=document.getElementById('task-list-search-input'); search.value='login'; search.dispatchEvent(new Event('input')); 'searched'`)
		b.waitFor("only matching rows including eighth task", visible, `["login-first","login-extra"]`)
		b.evaluate(`search.value=''; search.dispatchEvent(new Event('input')); document.querySelector('.task-list-more').click(); 'expanded'`)
		b.waitFor("show more reveals all nine rows", visible, `["login-first","other-2","other-3","other-4","other-5","other-6","other-7","login-extra","unrelated-extra"]`)
		b.evaluate(`search.value='login'; search.dispatchEvent(new Event('input')); 'searched expanded group'`)
		b.waitFor("expanded group still excludes nonmatches", visible, `["login-first","login-extra"]`)
		b.evaluate(`search.value=''; search.dispatchEvent(new Event('input')); 'cleared'`)
		b.waitFor("clearing search preserves show more", visible, `["login-first","other-2","other-3","other-4","other-5","other-6","other-7","login-extra","unrelated-extra"]`)
	})
}
