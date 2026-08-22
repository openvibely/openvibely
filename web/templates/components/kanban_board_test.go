package components

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestActiveColumnContent_GroupsOnlyRunningTasksInProgress(t *testing.T) {
	tasks := []models.Task{
		{ID: "running", ProjectID: "new-project", Title: "Running task", Category: models.CategoryActive, Status: models.StatusRunning},
		{ID: "pending", ProjectID: "new-project", Title: "Pending task", Category: models.CategoryActive, Status: models.StatusPending},
		{ID: "queued", ProjectID: "new-project", Title: "Queued task", Category: models.CategoryActive, Status: models.StatusQueued},
	}

	var buf bytes.Buffer
	if err := activeColumnContent(tasks, "new-project", nil, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render active column: %v", err)
	}
	html := buf.String()
	inProgressStart := strings.Index(html, ">In Progress</h4>")
	queuedStart := strings.Index(html, ">Queued</h4>")
	if inProgressStart < 0 || queuedStart < 0 || queuedStart <= inProgressStart {
		t.Fatalf("active column is missing ordered In Progress and Queued sections")
	}
	inProgressHTML := html[inProgressStart:queuedStart]
	queuedHTML := html[queuedStart:]
	if !strings.Contains(inProgressHTML, `id="task-running"`) {
		t.Fatal("running task missing from In Progress section")
	}
	if strings.Contains(inProgressHTML, `id="task-pending"`) || strings.Contains(inProgressHTML, `id="task-queued"`) {
		t.Fatal("pending or queued task rendered in In Progress section")
	}
	if !strings.Contains(queuedHTML, `id="task-pending"`) || !strings.Contains(queuedHTML, `id="task-queued"`) {
		t.Fatal("pending and queued tasks must render in Queued section")
	}
}

func TestKanbanColumn_DropdownTriggersUseLabelForDesktopWebviewCompatibility(t *testing.T) {
	var buf bytes.Buffer
	err := KanbanColumn([]models.Task{}, "project-1", models.CategoryBacklog, "", "", nil, nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render backlog column: %v", err)
	}
	html := buf.String()

	if !strings.Contains(html, `<label tabindex="0" class="btn btn-xs btn-ghost`) ||
		!strings.Contains(html, `title="More actions" onclick="handleDropdownToggle(event)">`) {
		t.Fatal("expected backlog kebab trigger to use <label> for stable dropdown focus behavior")
	}
	if strings.Contains(html, `<button tabindex="0" class="btn btn-xs btn-ghost`) {
		t.Fatal("unexpected <button> dropdown trigger in backlog column")
	}

	buf.Reset()
	err = KanbanColumn([]models.Task{}, "project-1", models.CategoryCompleted, "", "", nil, nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render completed column: %v", err)
	}
	html = buf.String()
	if !strings.Contains(html, `<label tabindex="0" class="btn btn-xs btn-ghost`) ||
		!strings.Contains(html, `title="More actions" onclick="handleDropdownToggle(event)">`) {
		t.Fatal("expected completed kebab trigger to use <label> for stable dropdown focus behavior")
	}
	if strings.Contains(html, `<button tabindex="0" class="btn btn-xs btn-ghost`) {
		t.Fatal("unexpected <button> dropdown trigger in completed column")
	}
}

func TestKanbanColumn_BacklogPriorityExecuteActionsUsePriorityLabelsAndRoutes(t *testing.T) {
	tasks := []models.Task{
		{ID: "priority-4", ProjectID: "project-1", Title: "Urgent task", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 4},
		{ID: "priority-3", ProjectID: "project-1", Title: "High task", Category: models.CategoryBacklog, Status: models.StatusFailed, Priority: 3},
		{ID: "priority-2", ProjectID: "project-1", Title: "Normal task", Category: models.CategoryBacklog, Status: models.StatusCancelled, Priority: 2},
		{ID: "priority-1", ProjectID: "project-1", Title: "Low task", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 1},
	}

	body := renderKanbanColumnForTest(t, tasks)
	for _, tt := range []struct {
		priority int
		label    string
	}{
		{priority: 4, label: "Urgent"},
		{priority: 3, label: "High"},
		{priority: 2, label: "Normal"},
		{priority: 1, label: "Low"},
	} {
		t.Run(tt.label, func(t *testing.T) {
			if got := PriorityLabel(tt.priority); got != tt.label {
				t.Fatalf("test expectation drifted from PriorityLabel(%d): got %q want %q", tt.priority, got, tt.label)
			}
			wantURL := fmt.Sprintf(`/tasks/backlog/execute?project_id=project-1&amp;priority=%d`, tt.priority)
			if !strings.Contains(body, wantURL) {
				t.Fatalf("expected priority action URL %q in %s", wantURL, body)
			}
			wantLabel := fmt.Sprintf("Execute %s (1)", tt.label)
			if !strings.Contains(body, wantLabel) {
				t.Fatalf("expected priority action label %q in %s", wantLabel, body)
			}
		})
	}
}

func TestKanbanColumn_BacklogPriorityExecuteActionOmittedWithoutEligibleTasks(t *testing.T) {
	body := renderKanbanColumnForTest(t, []models.Task{
		{ID: "priority-4", ProjectID: "project-1", Title: "Urgent task", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 4},
		{ID: "priority-3", ProjectID: "project-1", Title: "High task", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 3},
		{ID: "priority-2", ProjectID: "project-1", Title: "Completed normal task", Category: models.CategoryBacklog, Status: models.StatusCompleted, Priority: 2},
		{ID: "priority-1", ProjectID: "project-1", Title: "Low task", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 1},
	})

	if strings.Contains(body, `/tasks/backlog/execute?project_id=project-1&amp;priority=2`) || strings.Contains(body, "Execute Normal") {
		t.Fatalf("normal priority action should be omitted when no priority-2 tasks are eligible, got %s", body)
	}
	for _, want := range []string{
		`/tasks/backlog/execute?project_id=project-1&amp;priority=4`,
		`/tasks/backlog/execute?project_id=project-1&amp;priority=3`,
		`/tasks/backlog/execute?project_id=project-1&amp;priority=1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected eligible priority action %q in %s", want, body)
		}
	}
}

func TestKanbanColumn_BacklogPriorityActionsDoNotHardcodePriorityLabels(t *testing.T) {
	source, err := os.ReadFile("kanban_board.templ")
	if err != nil {
		t.Fatalf("read kanban template source: %v", err)
	}
	body := string(source)
	for _, forbidden := range []string{
		"Execute Urgent",
		"Execute High",
		"Execute Normal",
		"Execute Low",
		"urgent priority backlog tasks",
		"high priority backlog tasks",
		"normal priority backlog tasks",
		"low priority backlog tasks",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("kanban backlog priority actions must derive labels from PriorityLabel, found hardcoded %q", forbidden)
		}
	}
	if !strings.Contains(body, "PriorityLabel(priority)") {
		t.Fatalf("expected kanban backlog priority actions to derive labels through PriorityLabel")
	}
}

func renderKanbanColumnForTest(t *testing.T, tasks []models.Task) string {
	t.Helper()
	var buf bytes.Buffer
	if err := KanbanColumn(tasks, "project-1", models.CategoryBacklog, "", "", nil, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render backlog column: %v", err)
	}
	return buf.String()
}
