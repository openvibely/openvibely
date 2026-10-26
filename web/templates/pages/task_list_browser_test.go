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

func TestBrowserFunctional_TaskListGoalEventsRefreshIcons(t *testing.T) {
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
		achieved.Store(true)
		// Goal service events omit project_id; they must still refresh this list.
		b.evaluate(`window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'task_goal_evaluated',task_id:'goal-task',goal_status:'achieved'}})); 'sent'`)
		b.waitFor("goal achieved icon", state, "goal-met")
		achieved.Store(false)
		b.evaluate(`window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'task_goal_cleared',task_id:'goal-task'}})); 'sent'`)
		b.waitFor("cleared goal restores completed icon", state, "completed")
	})
}
