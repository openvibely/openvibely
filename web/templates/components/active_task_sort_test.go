package components

import (
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

func TestActiveTaskSortOptions(t *testing.T) {
	now := time.Now()
	tasks := []models.Task{
		{ID: "z", Title: "Zebra", Priority: 4, CreatedAt: now},
		{ID: "a", Title: "apple", Priority: 1, CreatedAt: now.Add(-time.Hour)},
	}
	for _, tc := range []struct{ sort, first string }{
		{"", "z"}, {"title_asc", "a"}, {"title_desc", "z"},
		{"created_asc", "a"}, {"created_desc", "z"},
		{"priority_asc", "a"}, {"priority_desc", "z"},
	} {
		t.Run(tc.sort, func(t *testing.T) {
			got := sortActiveTasks(tasks, tc.sort)
			if got[0].ID != tc.first {
				t.Fatalf("first = %s, want %s", got[0].ID, tc.first)
			}
			if tasks[0].ID != "z" {
				t.Fatal("sort changed source tasks")
			}
		})
	}
}

func TestActiveBoardBulkSelectionMatchesVisibleCards(t *testing.T) {
	tasks := []models.Task{
		{ID: "running", Category: models.CategoryActive, Status: models.StatusRunning},
		{ID: "pending", Category: models.CategoryActive, Status: models.StatusPending},
		{ID: "scheduled-running", Category: models.CategoryScheduled, Status: models.StatusRunning},
		{ID: "automation-waiting", Category: models.CategoryScheduled, Status: models.StatusPending, AutomationCapacityQueued: true},
		{ID: "scheduled-idle", Category: models.CategoryScheduled, Status: models.StatusPending},
		{ID: "terminal", Category: models.CategoryActive, Status: models.StatusCompleted},
		{ID: "child", Category: models.CategoryActive, Status: models.StatusRunning, SwarmRole: models.SwarmRoleWorker},
		{ID: "blocked-parent", Category: models.CategoryActive, Status: models.StatusBlocked, SwarmRole: models.SwarmRoleParent},
	}
	got := ActiveBoardTasks(tasks)
	if len(got) != 4 {
		t.Fatalf("selected %d tasks, want 4: %+v", len(got), got)
	}
	for i, want := range []string{"running", "pending", "scheduled-running", "automation-waiting"} {
		if got[i].ID != want {
			t.Errorf("selected %s, want %s", got[i].ID, want)
		}
	}
}
