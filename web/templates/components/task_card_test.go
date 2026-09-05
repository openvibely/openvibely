package components

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestStatusLabelForTask(t *testing.T) {
	tests := []struct {
		name     string
		status   models.TaskStatus
		category models.TaskCategory
		want     string
	}{
		{"active pending shows Queued", models.StatusPending, models.CategoryActive, "Queued"},
		{"backlog pending shows Pending", models.StatusPending, models.CategoryBacklog, "Pending"},
		{"scheduled pending shows Scheduled", models.StatusPending, models.CategoryScheduled, "Scheduled"},
		{"completed pending shows Queued", models.StatusPending, models.CategoryCompleted, "Queued"},
		{"active running shows In Progress", models.StatusRunning, models.CategoryActive, "In Progress"},
		{"backlog running shows In Progress", models.StatusRunning, models.CategoryBacklog, "In Progress"},
		{"any completed shows Completed", models.StatusCompleted, models.CategoryActive, "Completed"},
		{"any failed shows Failed", models.StatusFailed, models.CategoryBacklog, "Failed"},
		{"any cancelled shows Cancelled", models.StatusCancelled, models.CategoryActive, "Cancelled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StatusLabelForTask(tt.status, tt.category)
			if got != tt.want {
				t.Errorf("StatusLabelForTask(%q, %q) = %q, want %q", tt.status, tt.category, got, tt.want)
			}
		})
	}
}

func TestStatusLabel(t *testing.T) {
	tests := []struct {
		status models.TaskStatus
		want   string
	}{
		{models.StatusPending, "Queued"},
		{models.StatusRunning, "In Progress"},
		{models.StatusCompleted, "Completed"},
		{models.StatusFailed, "Failed"},
		{models.StatusCancelled, "Cancelled"},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			got := StatusLabel(tt.status)
			if got != tt.want {
				t.Errorf("StatusLabel(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestTaskLatestDuration(t *testing.T) {
	tests := []struct {
		name       string
		executions []models.Execution
		want       string
	}{
		{
			name:       "empty executions returns empty",
			executions: []models.Execution{},
			want:       "",
		},
		{
			name: "returns duration from latest execution with duration",
			executions: []models.Execution{
				{ID: "e1", DurationMs: 5000},
				{ID: "e2", DurationMs: 12000},
			},
			want: "12s",
		},
		{
			name: "skips executions without duration",
			executions: []models.Execution{
				{ID: "e1", DurationMs: 3000},
				{ID: "e2", DurationMs: 0},
			},
			want: "3s",
		},
		{
			name: "formats minutes correctly",
			executions: []models.Execution{
				{ID: "e1", DurationMs: 125000}, // 2m 5s
			},
			want: "2m 5s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TaskLatestDuration(tt.executions)
			if got != tt.want {
				t.Errorf("TaskLatestDuration() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTaskThreadStatusIndicator_Completed(t *testing.T) {
	task := &models.Task{
		ID:     "task1",
		Status: models.StatusCompleted,
	}
	executions := []models.Execution{
		{ID: "e1", Status: models.ExecCompleted, DurationMs: 5000},
	}

	var buf bytes.Buffer
	err := TaskThreadStatusIndicator(task, executions).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	body := buf.String()
	if body == "" {
		t.Fatal("expected non-empty output for completed task")
	}
	if !bytes.Contains(buf.Bytes(), []byte("Task completed")) {
		t.Error("expected 'Task completed' text in output")
	}
	if !bytes.Contains(buf.Bytes(), []byte("text-success")) {
		t.Error("expected success styling class")
	}
	if !bytes.Contains(buf.Bytes(), []byte("5s")) {
		t.Error("expected duration '5s' in output")
	}
}

func TestTaskThreadStatusIndicator_Failed(t *testing.T) {
	task := &models.Task{
		ID:     "task1",
		Status: models.StatusFailed,
	}
	executions := []models.Execution{
		{ID: "e1", Status: models.ExecFailed, DurationMs: 3000},
	}

	var buf bytes.Buffer
	err := TaskThreadStatusIndicator(task, executions).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	body := buf.String()
	if body == "" {
		t.Fatal("expected non-empty output for failed task")
	}
	if !bytes.Contains(buf.Bytes(), []byte("Task failed")) {
		t.Error("expected 'Task failed' text in output")
	}
	if !bytes.Contains(buf.Bytes(), []byte("text-error")) {
		t.Error("expected error styling class")
	}
}

func TestTaskThreadStatusIndicator_Running_NoIndicator(t *testing.T) {
	task := &models.Task{
		ID:     "task1",
		Status: models.StatusRunning,
	}
	executions := []models.Execution{
		{ID: "e1", Status: models.ExecRunning},
	}

	var buf bytes.Buffer
	err := TaskThreadStatusIndicator(task, executions).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	body := buf.String()
	if body != "" {
		t.Errorf("expected empty output for running task, got %q", body)
	}
}

func TestTaskThreadStatusIndicator_Pending_NoIndicator(t *testing.T) {
	task := &models.Task{
		ID:     "task1",
		Status: models.StatusPending,
	}
	executions := []models.Execution{}

	var buf bytes.Buffer
	err := TaskThreadStatusIndicator(task, executions).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	body := buf.String()
	if body != "" {
		t.Errorf("expected empty output for pending task, got %q", body)
	}
}

func TestKanbanBoard_DoesNotRenderActiveCancelledTaskAsQueued(t *testing.T) {
	tasks := []models.Task{
		{
			ID:        "active-cancelled",
			ProjectID: "default",
			Title:     "Cancelled Active Orphan",
			Category:  models.CategoryActive,
			Status:    models.StatusCancelled,
		},
		{
			ID:        "active-queued",
			ProjectID: "default",
			Title:     "Real Queued Task",
			Category:  models.CategoryActive,
			Status:    models.StatusQueued,
		},
	}

	var buf bytes.Buffer
	if err := KanbanBoard(tasks, "default", "", "", nil, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render kanban board: %v", err)
	}
	body := buf.String()
	if strings.Contains(body, "Cancelled Active Orphan") {
		t.Fatalf("cancelled active orphan should not render in Active queued dropzone, got %s", body)
	}
	if !strings.Contains(body, "Real Queued Task") {
		t.Fatalf("real queued active task should still render, got %s", body)
	}
}

func TestKanbanBoard_RendersCapacityQueuedAutomationInPendingDropzone(t *testing.T) {
	tasks := []models.Task{
		{
			ID:                       "automation-capacity-queued",
			ProjectID:                "default",
			Title:                    "Capacity Queued Automation",
			Category:                 models.CategoryScheduled,
			Status:                   models.StatusPending,
			AutomationCapacityQueued: true,
		},
		{
			ID:         "automation-future",
			ProjectID:  "default",
			Title:      "Future Automation Schedule",
			Category:   models.CategoryScheduled,
			Status:     models.StatusPending,
			CreatedVia: "automation:automation-1:future",
		},
		{
			ID:        "ordinary-scheduled",
			ProjectID: "default",
			Title:     "Ordinary Scheduled Task",
			Category:  models.CategoryScheduled,
			Status:    models.StatusPending,
		},
	}

	var buf bytes.Buffer
	if err := KanbanBoard(tasks, "default", "", "", nil, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render kanban board: %v", err)
	}
	body := buf.String()
	pending := activeStatusDropZone(t, body, "pending")
	if !strings.Contains(pending, "Capacity Queued Automation") {
		t.Fatalf("capacity-queued Automation task should render in Active pending dropzone, got %s", pending)
	}
	if strings.Contains(body, "Ordinary Scheduled Task") {
		t.Fatalf("ordinary scheduled task should remain managed by the Schedule page, got %s", body)
	}
}

func TestKanbanBoard_RendersSwarmParentWithRunningChildInProgress(t *testing.T) {
	tests := []struct {
		name         string
		parentStatus models.TaskStatus
		childStatus  models.TaskStatus
	}{
		{
			name:         "pending parent with running child",
			parentStatus: models.StatusPending,
			childStatus:  models.StatusRunning,
		},
		{
			name:         "blocked parent with running child",
			parentStatus: models.StatusBlocked,
			childStatus:  models.StatusRunning,
		},
		{
			name:         "blocked parent with pending planner",
			parentStatus: models.StatusBlocked,
			childStatus:  models.StatusPending,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parentID := "swarm-parent"
			tasks := []models.Task{
				{
					ID:          parentID,
					ProjectID:   "default",
					Title:       "Swarm Parent",
					Category:    models.CategoryActive,
					Status:      tt.parentStatus,
					SwarmRole:   models.SwarmRoleParent,
					SwarmStatus: "coordinating",
					SwarmChildren: []models.Task{
						{
							ID:           "worker-active",
							ProjectID:    "default",
							Title:        "Active Worker",
							Category:     models.CategoryActive,
							Status:       tt.childStatus,
							ParentTaskID: &parentID,
							SwarmRole:    models.SwarmRoleWorker,
						},
					},
				},
			}

			var buf bytes.Buffer
			if err := KanbanBoard(tasks, "default", "", "", nil, nil).Render(context.Background(), &buf); err != nil {
				t.Fatalf("render kanban board: %v", err)
			}
			body := buf.String()
			inProgress := activeStatusDropZone(t, body, "running")
			queued := activeStatusDropZone(t, body, "pending")

			if !strings.Contains(inProgress, "Swarm Parent") {
				t.Fatalf("expected swarm parent with active child in In Progress dropzone, got %s", inProgress)
			}
			if strings.Contains(queued, "Swarm Parent") {
				t.Fatalf("swarm parent with active child should not render as queued, got %s", queued)
			}
		})
	}
}

func activeStatusDropZone(t *testing.T, body, status string) string {
	t.Helper()
	marker := `data-drop-type="status" data-status="` + status + `" data-category="active"`
	start := strings.Index(body, marker)
	if start == -1 {
		t.Fatalf("expected active status dropzone %q in %s", status, body)
	}
	next := strings.Index(body[start+len(marker):], `data-drop-type="status"`)
	if next == -1 {
		return body[start:]
	}
	return body[start : start+len(marker)+next]
}

func TestTaskCard_LazilyLoadsAuthoritativeMergeOptions(t *testing.T) {
	task := models.Task{
		ID:        "merge-card-task",
		ProjectID: "project-1",
		Title:     "Merge card task",
		Category:  models.CategoryCompleted,
		Status:    models.StatusCompleted,
	}
	var buf bytes.Buffer
	if err := TaskCard(task, "project-1", "completed", nil, nil).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	for _, want := range []string{
		`data-task-card-menu-trigger`,
		`data-task-card-merge-options`,
		`hx-get="/tasks/merge-card-task/card/merge-options?project_id=project-1"`,
		`hx-trigger="task-card-menu-open"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected task card merge menu contract %q, body=%s", want, body)
		}
	}
}

func TestTaskCardMergeOptionsRemainRefreshableAndExposeCreatePR(t *testing.T) {
	task := models.Task{ID: "merge-card-task", ProjectID: "project-1", Title: "Merge card task", MergeTargetBranch: "main"}
	var buf bytes.Buffer
	if err := TaskCardMergeOptions(&task, "project-1", true, false, nil, true).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	for _, want := range []string{
		`hx-get="/tasks/merge-card-task/card/merge-options?project_id=project-1"`,
		`hx-trigger="task-card-menu-open"`,
		`data-task-card-pr-action`,
		`data-merge-type="pr"`,
		`Create PR`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected authoritative card action contract %q, body=%s", want, body)
		}
	}
}

func TestTaskCardMergeOptionsExposeSharedLocalActionMetadata(t *testing.T) {
	task := models.Task{ID: "merge-card-task", ProjectID: "project-1", Title: "Merge card task", MergeTargetBranch: "main"}
	var buf bytes.Buffer
	if err := TaskCardMergeOptions(&task, "project-1", true, true, nil, true).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	for _, want := range []string{
		`data-merge-type="merge"`,
		`data-merge-type="ff"`,
		`data-merge-type="rebase"`,
		`data-merge-type="squash"`,
		`data-merge-label="Merge commit"`,
		`data-merge-label="Fast-forward only"`,
		`data-merge-label="Rebase"`,
		`data-merge-label="Squash merge"`,
		`data-merge-endpoint="merge"`,
		`data-merge-endpoint="rebase"`,
		`data-target-branch="main"`,
		`Rebase onto main`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected shared card merge metadata %q, body=%s", want, body)
		}
	}
	for _, unwanted := range []string{"<details", "<summary", ">Merge</summary>"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("card merge actions must be flat menu rows without an expandable section, found %q in %s", unwanted, body)
		}
	}
}

func TestTaskCardMergeOptionsHideUnavailableActions(t *testing.T) {
	task := models.Task{ID: "merge-card-task", ProjectID: "project-1", Title: "Merge card task", MergeTargetBranch: "main"}
	var buf bytes.Buffer
	if err := TaskCardMergeOptions(&task, "project-1", false, false, nil, false).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	for _, unwanted := range []string{"Merge unavailable", "Create PR unavailable", `aria-disabled="true"`, `title="The task is not eligible."`, `title="No branch exists."`} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("ineligible card actions must be omitted rather than shown as unavailable, found %q in %s", unwanted, body)
		}
	}
	for _, want := range []string{`data-task-card-merge-options`, `hx-trigger="task-card-menu-open"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("empty authoritative options fragment must remain refreshable, missing %q in %s", want, body)
		}
	}
}

func TestTaskWorktreeMergeActionsDefineCanonicalLocalMapping(t *testing.T) {
	actions := TaskWorktreeMergeActions()
	want := []struct {
		mergeType        string
		label            string
		endpoint         string
		includeTarget    bool
		requiresRebase   bool
		includeMergeType bool
	}{
		{mergeType: "merge", label: "Merge commit", endpoint: "merge", includeMergeType: true},
		{mergeType: "ff", label: "Fast-forward only", endpoint: "merge", includeMergeType: true},
		{mergeType: "rebase", label: "Rebase", endpoint: "rebase", includeTarget: true, requiresRebase: true},
		{mergeType: "squash", label: "Squash merge", endpoint: "merge", includeMergeType: true},
	}
	if len(actions) != len(want) {
		t.Fatalf("expected %d local merge actions, got %d", len(want), len(actions))
	}
	for i, expected := range want {
		got := actions[i]
		if got.MergeType != expected.mergeType || got.Label != expected.label || got.Endpoint != expected.endpoint || got.IncludeTarget != expected.includeTarget || got.RequiresRebaseEligibility != expected.requiresRebase || got.IncludeMergeType != expected.includeMergeType {
			t.Fatalf("action %d mismatch: got %#v, want %#v", i, got, expected)
		}
	}
	if got := actions[2].DisplayLabel("develop"); got != "Rebase onto develop" {
		t.Fatalf("target-aware Rebase label = %q", got)
	}
	if got := TaskWorktreeMergeTarget(&models.Task{MergeTargetBranch: "develop"}); got != "develop" {
		t.Fatalf("target display = %q", got)
	}
	if got := TaskWorktreeMergeTarget(nil); got != "main" {
		t.Fatalf("empty target display = %q", got)
	}
}
func TestTaskCardMergeOptionsClosedHistoricalPRExposesCreatePR(t *testing.T) {
	task := models.Task{ID: "merge-card-task", ProjectID: "project-1", Title: "Merge card task", MergeTargetBranch: "main"}
	closedPR := &models.TaskPullRequest{TaskID: task.ID, PRNumber: 17, PRURL: "https://github.com/example/repo/pull/17", PRState: "closed"}
	var buf bytes.Buffer
	if err := TaskCardMergeOptions(&task, "project-1", true, false, closedPR, true).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	if strings.Contains(body, "View PR #17") {
		t.Fatalf("closed historical PR must not suppress current Create PR action: %s", body)
	}
	for _, want := range []string{`data-task-card-pr-action`, `data-merge-type="pr"`, `Create PR`} {
		if !strings.Contains(body, want) {
			t.Fatalf("closed historical PR should expose %q: %s", want, body)
		}
	}
}

func TestTaskCard_GoalMetIconMatchesDocumentedStandardGlyph(t *testing.T) {
	doc, err := os.ReadFile("../../../docs/task-status-icon-options.html")
	if err != nil {
		t.Fatalf("read task status icon options: %v", err)
	}
	for _, want := range []string{
		`{ key: 'goal-met', label: 'Goal met' }`,
		`standardGoal: {`,
		`'goal-met': '<circle cx="12" cy="8.5" r="7.5"/><path d="m8 14-2 8 6-3.5 6 3.5-2-8"/><path d="m9 8.5 2 2 4-4"/>'`,
		`goalSource: 'standardGoal'`,
	} {
		if !bytes.Contains(doc, []byte(want)) {
			t.Fatalf("task status icon options must define the standard goal-met glyph %q", want)
		}
	}

	task := models.Task{ID: "goal-met", ProjectID: "default", Title: "Met goal", Category: models.CategoryCompleted, Status: models.StatusCompleted, GoalMet: true}
	var rendered bytes.Buffer
	if err := TaskCard(task, "default", "completed", nil, nil).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render goal-met task card: %v", err)
	}
	for _, want := range []string{
		`<circle cx="12" cy="8.5" r="7.5" stroke-width="2"></circle>`,
		`d="m8 14-2 8 6-3.5 6 3.5-2-8"`,
		`d="m9 8.5 2 2 4-4"`,
	} {
		if !strings.Contains(rendered.String(), want) {
			t.Fatalf("rendered goal-met icon must use documented geometry %q, got %s", want, rendered.String())
		}
	}
}

func TestTaskCard_RendersPersistentAccessibleStateIconBeforeTitle(t *testing.T) {
	tests := []struct {
		name        string
		status      models.TaskStatus
		category    models.TaskCategory
		mergeStatus models.MergeStatus
		goalMet     bool
		wantState   string
		wantLabel   string
	}{
		{name: "backlog pending", status: models.StatusPending, category: models.CategoryBacklog, wantState: "pending", wantLabel: "Pending"},
		{name: "active pending", status: models.StatusPending, category: models.CategoryActive, wantState: "queued", wantLabel: "Queued"},
		{name: "queued", status: models.StatusQueued, category: models.CategoryActive, wantState: "queued", wantLabel: "Queued"},
		{name: "running", status: models.StatusRunning, category: models.CategoryActive, wantState: "running", wantLabel: "In Progress"},
		{name: "completed", status: models.StatusCompleted, category: models.CategoryCompleted, wantState: "completed", wantLabel: "Completed"},
		{name: "completed with met goal", status: models.StatusCompleted, category: models.CategoryCompleted, goalMet: true, wantState: "goal-met", wantLabel: "Goal met"},
		{name: "failed", status: models.StatusFailed, category: models.CategoryBacklog, wantState: "failed", wantLabel: "Failed"},
		{name: "cancelled", status: models.StatusCancelled, category: models.CategoryBacklog, wantState: "cancelled", wantLabel: "Cancelled"},
		{name: "blocked", status: models.StatusBlocked, category: models.CategoryActive, wantState: "blocked", wantLabel: "Waiting for Parent"},
		{name: "merged overrides completion", status: models.StatusCompleted, category: models.CategoryCompleted, mergeStatus: models.MergeStatusMerged, wantState: "merged", wantLabel: "Merged"},
		{name: "merged overrides met goal", status: models.StatusCompleted, category: models.CategoryCompleted, mergeStatus: models.MergeStatusMerged, goalMet: true, wantState: "merged", wantLabel: "Merged"},
		{name: "stale met goal does not override running", status: models.StatusRunning, category: models.CategoryActive, goalMet: true, wantState: "running", wantLabel: "In Progress"},
		{name: "stale merged does not override running", status: models.StatusRunning, category: models.CategoryActive, mergeStatus: models.MergeStatusMerged, wantState: "running", wantLabel: "In Progress"},
		{name: "stale merged does not override failed", status: models.StatusFailed, category: models.CategoryBacklog, mergeStatus: models.MergeStatusMerged, wantState: "failed", wantLabel: "Failed"},
		{name: "stale merged does not override cancelled", status: models.StatusCancelled, category: models.CategoryBacklog, mergeStatus: models.MergeStatusMerged, wantState: "cancelled", wantLabel: "Cancelled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := models.Task{
				ID:          "state-card",
				ProjectID:   "default",
				Title:       "State title",
				Category:    tt.category,
				Status:      tt.status,
				MergeStatus: tt.mergeStatus,
				GoalMet:     tt.goalMet,
			}
			var buf bytes.Buffer
			if err := TaskCard(task, "default", string(tt.category), nil, nil).Render(context.Background(), &buf); err != nil {
				t.Fatalf("render task card: %v", err)
			}
			body := buf.String()
			icon := `data-task-state-icon data-task-state="` + tt.wantState + `"`
			if !strings.Contains(body, icon) {
				t.Fatalf("expected %s state icon, got %s", tt.wantState, body)
			}
			for _, want := range []string{
				`role="img"`,
				`aria-label="Task state: ` + tt.wantLabel + `"`,
				`data-tip="` + tt.wantLabel + `"`,
				`tooltip-right`,
				`aria-hidden="true"`,
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("expected accessible state icon markup %q, got %s", want, body)
				}
			}
			if strings.Contains(body, `tooltip-bottom`) {
				t.Fatalf("state tooltip must open inward from the card's left edge, got %s", body)
			}
			if !strings.Contains(body, `</span><span data-task-title class="min-w-0 flex-1 break-words sm:truncate">State title</span>`) {
				t.Fatalf("state icon must render immediately before the shrink-safe title, got %s", body)
			}
		})
	}
}

func TestKanbanBoard_RendersStateIconsInEveryCardVariant(t *testing.T) {
	tasks := []models.Task{
		{ID: "backlog-failed", ProjectID: "default", Title: "Failed card", Category: models.CategoryBacklog, Status: models.StatusFailed},
		{ID: "active-queued", ProjectID: "default", Title: "Queued card", Category: models.CategoryActive, Status: models.StatusQueued},
		{ID: "active-running", ProjectID: "default", Title: "Running card", Category: models.CategoryActive, Status: models.StatusRunning},
		{ID: "completed-merged", ProjectID: "default", Title: "Merged card", Category: models.CategoryCompleted, Status: models.StatusCompleted, MergeStatus: models.MergeStatusMerged},
	}
	var buf bytes.Buffer
	if err := KanbanBoard(tasks, "default", "", "", nil, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render kanban board: %v", err)
	}
	body := buf.String()
	for _, state := range []string{"failed", "queued", "running", "merged"} {
		if !strings.Contains(body, `data-task-state-icon data-task-state="`+state+`"`) {
			t.Fatalf("expected %s icon in rendered board variants, got %s", state, body)
		}
	}
}

func TestTaskCard_UsesGrabCursorForDrag(t *testing.T) {
	task := models.Task{
		ID:        "task-1",
		ProjectID: "default",
		Title:     "Drag me",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
	}

	var buf bytes.Buffer
	if err := TaskCard(task, "default", "", nil, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render task card: %v", err)
	}
	body := buf.String()
	for _, want := range []string{"cursor-grab", "active:cursor-grabbing", "drag-cursor-surface", `onpointerdown="handleTaskPointerDown(event)"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected pointer-draggable task card to contain %q, got %s", want, body)
		}
	}
	for _, forbidden := range []string{"cursor-move", `draggable="true"`, `ondragstart=`, `ondragend=`, "drag-card-preview", "drag-cursor-indicator"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("task card should preserve native drag with grab cursor styling, found %q in %s", forbidden, body)
		}
	}
}

func TestTaskCard_HasMobileSafeActionsAndReadableText(t *testing.T) {
	task := models.Task{
		ID:        "task-1",
		ProjectID: "default",
		Title:     "A very long task title that should wrap on mobile instead of overflowing the viewport or disappearing behind controls",
		Prompt:    "A very long task prompt that should wrap safely on narrow task cards so the card remains readable without horizontal scrolling.",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
	}

	var buf bytes.Buffer
	if err := TaskCard(task, "default", "", nil, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render task card: %v", err)
	}
	body := buf.String()
	cardStart := strings.Index(body, `id="task-task-1"`)
	if cardStart == -1 {
		t.Fatalf("expected rendered task card root, got %s", body)
	}
	cardEnd := strings.Index(body[cardStart:], `>`)
	if cardEnd == -1 {
		t.Fatalf("expected rendered task card opening tag, got %s", body)
	}
	cardTag := body[cardStart : cardStart+cardEnd]
	if !strings.Contains(cardTag, "overflow-visible") {
		t.Fatalf("task card root must allow kebab menus to render outside card bounds, got %s", cardTag)
	}
	if strings.Contains(cardTag, "overflow-hidden") {
		t.Fatalf("task card root must not clip kebab menus with overflow-hidden, got %s", cardTag)
	}

	for _, want := range []string{
		"min-w-0",
		"overflow-visible",
		"min-h-11",
		"h-11",
		"w-11",
		"pt-14",
		"sm:pt-4",
		"pr-0",
		"sm:pr-24",
		"lg:pr-12",
		"break-words",
		"sm:truncate",
		"line-clamp-3",
		"sm:line-clamp-2",
		"lg:min-h-6",
		"lg:h-6",
		"lg:w-6",
		"max-w-[calc(100vw-2rem)]",
		`class="text-sm min-h-11"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected mobile-safe task card markup to contain %q, got %s", want, body)
		}
	}
}

func TestTaskCard_SwarmAccordionIsFullWidthAndCollapsedByDefault(t *testing.T) {
	childID := "worker-1"
	task := models.Task{
		ID:        "parent-1",
		ProjectID: "default",
		Title:     "Swarm parent",
		Category:  models.CategoryActive,
		Status:    models.StatusRunning,
		SwarmRole: models.SwarmRoleParent,
		SwarmChildren: []models.Task{
			{
				ID:           childID,
				ProjectID:    "default",
				Title:        "Worker child",
				Category:     models.CategoryCompleted,
				Status:       models.StatusCompleted,
				SwarmRole:    models.SwarmRoleWorker,
				ParentTaskID: &childID,
			},
		},
	}

	var buf bytes.Buffer
	if err := TaskCard(task, "default", "", nil, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render task card: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, `<details class="group mt-3 w-full`) {
		t.Fatalf("expected swarm accordion to render full-width details container, got %s", body)
	}
	if strings.Contains(body, `<details class="group mt-3 w-full rounded-xl border border-base-300 bg-base-200/45 text-xs shadow-inner overflow-hidden" open`) {
		t.Fatalf("expected swarm accordion to be collapsed by default, got %s", body)
	}
	if !strings.Contains(body, "1 worker") || !strings.Contains(body, "1/1 done") {
		t.Fatalf("expected existing swarm summary labels in accordion header, got %s", body)
	}
}

func TestTaskCard_RendersGoalBadge(t *testing.T) {
	task := models.Task{
		ID:        "task-1",
		ProjectID: "default",
		Title:     "Goal task",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		HasGoal:   true,
	}

	var buf bytes.Buffer
	if err := TaskCard(task, "default", "", nil, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render task card: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, "Task has an active goal") {
		t.Fatalf("expected goal badge title in task card, got %s", body)
	}
	if !strings.Contains(body, ">Goal<") {
		t.Fatalf("expected goal badge label in task card, got %s", body)
	}
}

func TestTaskCard_RendersPriorityBadgeLabels(t *testing.T) {
	for _, tt := range []struct {
		priority int
		label    string
	}{
		{priority: 1, label: "Low"},
		{priority: 2, label: "Normal"},
		{priority: 3, label: "High"},
		{priority: 4, label: "Urgent"},
	} {
		t.Run(tt.label, func(t *testing.T) {
			if got := PriorityLabel(tt.priority); got != tt.label {
				t.Fatalf("test expectation drifted from PriorityLabel(%d): got %q want %q", tt.priority, got, tt.label)
			}
			task := models.Task{
				ID:        "task-priority-badge",
				ProjectID: "default",
				Title:     "Priority badge task",
				Category:  models.CategoryBacklog,
				Status:    models.StatusPending,
				Priority:  tt.priority,
			}

			var buf bytes.Buffer
			if err := TaskCard(task, "default", "", nil, nil).Render(context.Background(), &buf); err != nil {
				t.Fatalf("render task card: %v", err)
			}
			want := ">" + tt.label + "</span>"
			if !strings.Contains(buf.String(), want) {
				t.Fatalf("expected priority badge label %q in %s", tt.label, buf.String())
			}
		})
	}
}
