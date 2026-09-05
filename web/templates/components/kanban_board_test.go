package components

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestKanbanBoardRefreshPreservesSelectionAndOpenMenus(t *testing.T) {
	var buf bytes.Buffer
	tasks := []models.Task{{ID: "selected-task", ProjectID: "project-1", Title: "Selected", Category: models.CategoryBacklog, Status: models.StatusPending}}
	if err := KanbanBoard(tasks, "project-1", "", "", nil, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render kanban board: %v", err)
	}
	html := buf.String()
	for _, required := range []string{
		`data-kanban-menu-key="column-backlog"`,
		`data-kanban-menu-key="column-completed"`,
		`data-kanban-menu-key="task-selected-task"`,
		`data-kanban-menu-trigger`,
		`aria-expanded="false"`,
		`data-kanban-menu-content`,
		`<button type="button" tabindex="0"`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("kanban refresh state requires rendered contract %q", required)
		}
	}

	for _, unsupported := range []string{`aria-haspopup="menu"`, `role="menu"`} {
		if strings.Contains(html, unsupported) {
			t.Fatalf("kanban dropdown must not claim unsupported ARIA menu semantics %q", unsupported)
		}
	}

	if strings.Contains(html, `<a tabindex="0"`) {
		t.Fatal("column HTMX actions must use native keyboard-operable buttons, not focusable anchors without href")
	}

	layoutSource, err := os.ReadFile(filepath.Join("..", "layout", "base.templ"))
	if err != nil {
		t.Fatalf("read base template: %v", err)
	}
	for _, required := range []string{"savedKanbanInteraction", "restoreKanbanInteraction", "selectedTaskIDs", "focusKey", "aria-expanded"} {
		if !bytes.Contains(layoutSource, []byte(required)) {
			t.Fatalf("kanban refresh script must preserve %q", required)
		}
	}
}

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
		!strings.Contains(html, `title="More actions" onclick="handleDropdownToggle(event)"`) ||
		!strings.Contains(html, `data-kanban-menu-trigger aria-label="More actions" aria-expanded="false"`) {
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
		!strings.Contains(html, `title="More actions" onclick="handleDropdownToggle(event)"`) ||
		!strings.Contains(html, `data-kanban-menu-trigger aria-label="More actions" aria-expanded="false"`) {
		t.Fatal("expected completed kebab trigger to use <label> for stable dropdown focus behavior")
	}
	if strings.Contains(html, `<button tabindex="0" class="btn btn-xs btn-ghost`) {
		t.Fatal("unexpected <button> dropdown trigger in completed column")
	}
}

func TestKanbanColumn_BacklogExecuteAllActionHasNoActivationOrConfirmation(t *testing.T) {
	body := renderKanbanColumnForTest(t, []models.Task{{
		ID:        "eligible-backlog",
		ProjectID: "project-1",
		Title:     "Eligible backlog task",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
	}})

	if strings.Contains(body, "Activate All") || strings.Contains(body, "/tasks/backlog/activate") {
		t.Fatalf("backlog menu must not render the redundant Activate All action: %s", body)
	}

	actionStart := strings.Index(body, `hx-post="/tasks/backlog/execute?project_id=project-1"`)
	if actionStart < 0 {
		t.Fatalf("backlog menu is missing the Execute All request: %s", body)
	}
	actionEnd := strings.Index(body[actionStart:], "</button>")
	if actionEnd < 0 {
		t.Fatalf("backlog Execute All action is missing its closing button: %s", body)
	}
	action := body[actionStart : actionStart+actionEnd]
	if !strings.Contains(action, "Execute All (1)") {
		t.Fatalf("backlog Execute All action has unexpected markup: %s", action)
	}
	if strings.Contains(action, "hx-confirm") {
		t.Fatalf("backlog Execute All action must submit without confirmation: %s", action)
	}
	for _, required := range []string{`hx-target="#kanban-board"`, `hx-swap="outerHTML"`} {
		if !strings.Contains(action, required) {
			t.Fatalf("backlog Execute All action must preserve %s: %s", required, action)
		}
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

func TestKanbanColumn_DeleteAllActionsOpenSharedConfirmation(t *testing.T) {
	cases := []struct {
		category  models.TaskCategory
		name      string
		ariaLabel string
	}{
		{models.CategoryCompleted, "completed tasks", "Delete all completed tasks"},
		{models.CategoryBacklog, "backlog tasks", "Delete all backlog tasks"},
	}

	for _, tc := range cases {
		t.Run(string(tc.category), func(t *testing.T) {
			body := renderKanbanColumnForCategoryTest(t, tc.category, []models.Task{{
				ID:        "task-1",
				ProjectID: "project-1",
				Title:     "Task to delete",
				Category:  tc.category,
				Status:    models.StatusCompleted,
			}})

			for _, want := range []string{
				`data-delete-all-tasks-category="` + string(tc.category) + `"`,
				`data-delete-all-tasks-name="` + tc.name + `"`,
				`data-project-id="project-1"`,
				`aria-label="` + tc.ariaLabel + `"`,
				`onclick="openDeleteAllTasksConfirm(this)"`,
				`Delete All</button>`,
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("expected %s delete action to contain %q, got %s", tc.category, want, body)
				}
			}
			if strings.Contains(body, `hx-confirm="Are you sure you want to delete all`) {
				t.Fatalf("%s delete-all action must not use the browser confirmation attribute", tc.category)
			}
			if strings.Contains(body, `hx-delete="/tasks/`+string(tc.category)) {
				t.Fatalf("%s delete-all action must not delete before the shared modal is confirmed", tc.category)
			}
		})
	}
}

func renderKanbanColumnForTest(t *testing.T, tasks []models.Task) string {
	return renderKanbanColumnForCategoryTest(t, models.CategoryBacklog, tasks)
}

func renderKanbanColumnForCategoryTest(t *testing.T, category models.TaskCategory, tasks []models.Task) string {
	t.Helper()
	var buf bytes.Buffer
	if err := KanbanColumn(tasks, "project-1", category, "", "", nil, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render %s column: %v", category, err)
	}
	return buf.String()
}
