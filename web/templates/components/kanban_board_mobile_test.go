package components

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestKanbanBoardUsesResponsiveNonScrollingLayout(t *testing.T) {
	var buf bytes.Buffer
	if err := KanbanBoard(nil, "project-1", "", "", nil, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render kanban board: %v", err)
	}

	body := buf.String()
	for _, want := range []string{
		`id="kanban-board"`,
		"grid-cols-1",
		"md:grid-cols-2",
		"lg:grid-cols-3",
		"overflow-x-hidden",
		"overflow-y-auto",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected kanban board markup to contain %q, got %s", want, body)
		}
	}
	if strings.Contains(body, "overflow-x-auto") {
		t.Fatalf("kanban board should not force horizontal scrolling on mobile, got %s", body)
	}
	if strings.Contains(body, "lg:grid-cols-4") {
		t.Fatalf("kanban board should not reserve a phantom fourth desktop column, got %s", body)
	}
}

func TestKanbanBoardDoesNotRenderActiveCancelledTaskAsQueued(t *testing.T) {
	tasks := []models.Task{
		{
			ID:        "task-active-cancelled",
			ProjectID: "project-1",
			Title:     "Hidden cancelled active task",
			Category:  models.CategoryActive,
			Status:    models.StatusCancelled,
			Priority:  2,
		},
	}

	var buf bytes.Buffer
	if err := KanbanBoard(tasks, "project-1", "", "", nil, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render kanban board: %v", err)
	}

	body := buf.String()
	if strings.Contains(body, "Hidden cancelled active task") || strings.Contains(body, `data-task-status="cancelled"`) {
		t.Fatalf("active cancelled task should not render as queued active-column markup, got %s", body)
	}
}

func TestKanbanColumnHasMobileSafeWidthAndTouchMenu(t *testing.T) {
	tasks := []models.Task{
		{
			ID:        "task-1",
			ProjectID: "project-1",
			Title:     "Urgent backlog task",
			Category:  models.CategoryBacklog,
			Status:    models.StatusPending,
			Priority:  4,
		},
	}

	var buf bytes.Buffer
	if err := KanbanColumn(tasks, "project-1", models.CategoryBacklog, "", "", nil, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render kanban column: %v", err)
	}

	body := buf.String()
	for _, want := range []string{
		"kanban-column",
		"w-full",
		// Mobile height: fixed dvh-based height so each dropzone is independently scrollable
		// and at least two task cards are visible in the viewport.
		"h-[60dvh]",
		"min-h-[24rem]",
		// Desktop override: full-height grid layout, clearing the mobile fixed height.
		"lg:h-full",
		"lg:min-h-0",
		"min-h-11",
		"h-11",
		"w-11",
		"max-w-[calc(100vw-2rem)]",
		`class="text-sm min-h-11`,
		`/tasks/backlog/execute?project_id=project-1`,
		`class="text-sm pl-8 min-h-11`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected responsive kanban column markup to contain %q, got %s", want, body)
		}
	}

	// Old 18rem minimum was too short to show two task cards; ensure it is gone.
	if strings.Contains(body, "min-h-[18rem]") {
		t.Fatalf("kanban column must not use old min-h-[18rem] which is too short to show two task cards on mobile, got %s", body)
	}
	// The dropzone inside the column must be independently scrollable.
	if !strings.Contains(body, "overflow-y-auto") {
		t.Fatalf("kanban column dropzone must have overflow-y-auto for independent scroll on mobile, got %s", body)
	}

	executeIdx := strings.Index(body, `/tasks/backlog/execute?project_id=project-1`)
	if executeIdx == -1 {
		t.Fatalf("expected backlog Execute All action in markup, got %s", body)
	}
	executeSnippet := body[executeIdx:]
	if len(executeSnippet) > 500 {
		executeSnippet = executeSnippet[:500]
	}
	if !strings.Contains(executeSnippet, `class="text-sm min-h-11"`) {
		t.Fatalf("expected backlog Execute All action to have touch-friendly min height, got %s", executeSnippet)
	}
	if strings.Contains(executeSnippet, "hx-confirm") {
		t.Fatalf("expected backlog Execute All action to submit without confirmation, got %s", executeSnippet)
	}
	if strings.Contains(body, "Activate All") || strings.Contains(body, `/tasks/backlog/activate?project_id=project-1`) {
		t.Fatalf("backlog menu must not render the removed Activate All action, got %s", body)
	}
}
