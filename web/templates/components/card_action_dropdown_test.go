package components

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestTaskCardActionDropdownKeepsKanbanAttributesAndActions(t *testing.T) {
	task := models.Task{ID: "task-1", ProjectID: "project-1", Title: "Ship feature", Category: models.CategoryBacklog, Status: models.StatusPending}
	var buf bytes.Buffer
	if err := TaskCard(task, "project-1", "backlog", nil, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render task card: %v", err)
	}
	html := buf.String()
	for _, want := range []string{
		`data-kanban-menu-key="task-task-1"`,
		`data-kanban-menu-trigger`,
		`data-task-card-menu-trigger`,
		`data-kanban-menu-content`,
		`aria-expanded="false"`,
		`aria-label="More actions for Ship feature"`,
		`onclick="handleDropdownToggle(event)"`,
		`onclick="event.stopPropagation()"`,
		`Run`,
		`Edit`,
		`/tasks/task-1?tab=details&amp;from=tasks`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected task card menu to contain %q", want)
		}
	}
}

func TestKanbanColumnActionDropdownKeepsSortAttributesAndActions(t *testing.T) {
	tasks := []models.Task{{ID: "done-1", ProjectID: "project-1", Title: "Done", Category: models.CategoryCompleted, Status: models.StatusCompleted}}
	html := renderKanbanColumnForCategoryTest(t, models.CategoryCompleted, tasks)
	for _, want := range []string{
		`data-kanban-menu-key="column-completed"`,
		`data-kanban-menu-trigger`,
		`data-kanban-menu-content`,
		`aria-expanded="false"`,
		`aria-label="More actions"`,
		`onclick="handleDropdownToggle(event)"`,
		`Sort By`,
		`Name (A-Z)`,
		`Date (Newest First)`,
		`Priority (High to Low)`,
		`data-delete-all-tasks-category="completed"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected completed column menu to contain %q", want)
		}
	}
}
