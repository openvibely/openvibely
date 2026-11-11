package layout

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestTaskListGroupsScheduledLifecycle(t *testing.T) {
	for _, tt := range []struct {
		name             string
		status           models.TaskStatus
		workerQueued     bool
		automationQueued bool
		want             string
	}{
		{name: "waiting for schedule", status: models.StatusPending, want: "Scheduled"},
		{name: "queued", status: models.StatusQueued, want: "Active"},
		{name: "waiting for worker", status: models.StatusPending, workerQueued: true, want: "Active"},
		{name: "waiting for automation worker", status: models.StatusPending, automationQueued: true, want: "Active"},
		{name: "running", status: models.StatusRunning, want: "Active"},
		{name: "completed", status: models.StatusCompleted, want: "Scheduled"},
		{name: "failed", status: models.StatusFailed, want: "Scheduled"},
		{name: "cancelled", status: models.StatusCancelled, want: "Scheduled"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tasks := []models.Task{{ID: "scheduled", Category: models.CategoryScheduled, Status: tt.status, WorkerCapacityQueued: tt.workerQueued, AutomationCapacityQueued: tt.automationQueued}}
			groups := TaskListGroups(tasks)
			if len(groups) != 1 || groups[0].Label != tt.want || len(groups[0].Tasks) != 1 || groups[0].Tasks[0].ID != "scheduled" {
				t.Fatalf("unexpected groups: %+v", groups)
			}
			if tasks[0].Category != models.CategoryScheduled || groups[0].Tasks[0].Category != models.CategoryScheduled {
				t.Fatal("sidebar grouping changed the saved category")
			}
		})
	}
}

func TestTaskListGroupsPreservesOrderAndExcludesChildren(t *testing.T) {
	parent := "scheduled"
	tasks := []models.Task{
		{ID: "active", Category: models.CategoryActive},
		{ID: "scheduled", Category: models.CategoryScheduled, Status: models.StatusRunning},
		{ID: "child", Category: models.CategoryScheduled, Status: models.StatusRunning, ParentTaskID: &parent},
		{ID: "completed", Category: models.CategoryCompleted},
		{ID: "backlog", Category: models.CategoryBacklog},
		{ID: "waiting", Category: models.CategoryScheduled, Status: models.StatusPending},
	}
	groups := TaskListGroups(tasks)
	labels := []string{"Active", "Completed", "Backlog", "Scheduled"}
	if len(groups) != len(labels) {
		t.Fatalf("unexpected groups: %+v", groups)
	}
	for i, label := range labels {
		if groups[i].Label != label {
			t.Fatalf("group %d = %q, want %q", i, groups[i].Label, label)
		}
	}
	if len(groups[0].Tasks) != 2 || groups[0].Tasks[0].ID != "active" || groups[0].Tasks[1].ID != "scheduled" {
		t.Fatalf("unexpected active tasks: %+v", groups[0].Tasks)
	}
	if len(groups[3].Tasks) != 1 || groups[3].Tasks[0].ID != "waiting" {
		t.Fatalf("unexpected scheduled tasks: %+v", groups[3].Tasks)
	}
}

func TestTaskListOpenPullRequestIcon(t *testing.T) {
	tasks := []models.Task{{ID: "review", ProjectID: "default", Title: "Review", Category: models.CategoryCompleted, Status: models.StatusCompleted, HasOpenPullRequest: true}}
	var out bytes.Buffer
	if err := TaskListPanelContent("default", TaskListGroups(tasks)).Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `data-task-state="pr-open"`) {
		t.Fatal("missing PR icon in navigation")
	}
}
