package components

import (
	"github.com/openvibely/openvibely/internal/models"
	"testing"
)

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
