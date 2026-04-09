package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

func TestTaskDetailMetrics_StatusBadgeVisibility(t *testing.T) {
	tests := []struct {
		name               string
		status             models.TaskStatus
		category           models.TaskCategory
		shouldShowStatus   bool
		expectedStatusText string
	}{
		{
			name:             "backlog pending hides status badge",
			status:           models.StatusPending,
			category:         models.CategoryBacklog,
			shouldShowStatus: false,
		},
		{
			name:             "scheduled pending hides status badge",
			status:           models.StatusPending,
			category:         models.CategoryScheduled,
			shouldShowStatus: false,
		},
		{
			name:               "active pending shows status badge",
			status:             models.StatusPending,
			category:           models.CategoryActive,
			shouldShowStatus:   true,
			expectedStatusText: "Queued",
		},
		{
			name:               "backlog running shows status badge",
			status:             models.StatusRunning,
			category:           models.CategoryBacklog,
			shouldShowStatus:   true,
			expectedStatusText: "In Progress",
		},
		{
			name:               "backlog completed shows status badge",
			status:             models.StatusCompleted,
			category:           models.CategoryBacklog,
			shouldShowStatus:   true,
			expectedStatusText: "Completed",
		},
		{
			name:               "backlog failed shows status badge",
			status:             models.StatusFailed,
			category:           models.CategoryBacklog,
			shouldShowStatus:   true,
			expectedStatusText: "Failed",
		},
		{
			name:               "scheduled running shows status badge",
			status:             models.StatusRunning,
			category:           models.CategoryScheduled,
			shouldShowStatus:   true,
			expectedStatusText: "In Progress",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &models.Task{
				ID:       "task1",
				Title:    "Test Task",
				Status:   tt.status,
				Category: tt.category,
			}
			executions := []models.Execution{}

			var buf bytes.Buffer
			err := TaskDetailMetrics(task, executions).Render(context.Background(), &buf)
			if err != nil {
				t.Fatalf("render failed: %v", err)
			}

			output := buf.String()

			// Check if category badge is always shown
			if !strings.Contains(output, string(tt.category)) {
				t.Errorf("expected category %q to be shown", tt.category)
			}

			// Check if status badge visibility matches expectation
			hasStatusLabel := strings.Contains(output, "Status:")
			if hasStatusLabel != tt.shouldShowStatus {
				t.Errorf("status badge visibility = %v, want %v", hasStatusLabel, tt.shouldShowStatus)
			}

			// If status should be shown, verify the correct label appears
			if tt.shouldShowStatus && tt.expectedStatusText != "" {
				if !strings.Contains(output, tt.expectedStatusText) {
					t.Errorf("expected status text %q not found in output", tt.expectedStatusText)
				}
			}
		})
	}
}

func TestTaskDetailContent_ChangesTabHidesReviewCommentCountBadge(t *testing.T) {
	task := &models.Task{
		ID:       "task-1",
		Title:    "Task",
		ProjectID:"project-1",
		Status:   models.StatusCompleted,
		Category: models.CategoryCompleted,
	}
	reviewComments := []models.ReviewComment{{ID: "c1", CommentText: "x"}}

	var buf bytes.Buffer
	err := TaskDetailContent(task, nil, nil, nil, nil, nil, "changes", reviewComments).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, ">Changes</a>") {
		t.Fatal("expected Changes tab to render")
	}
	if strings.Contains(output, "badge badge-warning badge-xs") {
		t.Fatal("did not expect Changes tab review comment count badge")
	}
}

func TestTaskDetailContent_ReactivatesFileChangesSSEWhenTaskBecomesActive(t *testing.T) {
	task := &models.Task{
		ID:       "task-2",
		Title:    "Task",
		ProjectID:"project-1",
		Status:   models.StatusCompleted,
		Category: models.CategoryCompleted,
	}

	var buf bytes.Buffer
	err := TaskDetailContent(task, nil, nil, nil, nil, nil, "changes", nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "var nowActive = (status === 'running' || status === 'queued');") {
		t.Fatal("expected status watcher to calculate active task states")
	}
	if !strings.Contains(output, "if (!wasActive && nowActive && _fileChangesTaskId && _isChangesTabActive()) {") {
		t.Fatal("expected status watcher to restart file changes SSE only when changes tab is active")
	}
	if !strings.Contains(output, "_startFileChangesSSE(_fileChangesTaskId);") {
		t.Fatal("expected status watcher to call _startFileChangesSSE for reactivated follow-up runs")
	}
}

func TestTaskDetailContent_FileChangesRefreshRequiresActiveTab(t *testing.T) {
	task := &models.Task{
		ID:        "task-3",
		Title:     "Task",
		ProjectID: "project-1",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}

	var buf bytes.Buffer
	err := TaskDetailContent(task, nil, nil, nil, nil, nil, "details", nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "function _isChangesTabActive() {") {
		t.Fatal("expected helper for checking active changes tab")
	}
	if !strings.Contains(output, "if (!_isChangesTabActive()) return;") {
		t.Fatal("expected diff viewer refresh to no-op when changes tab is inactive")
	}
	if !strings.Contains(output, "if (triggerEl.id === 'changes-content' && !_isChangesTabActive()) {") {
		t.Fatal("expected beforeRequest guard to block hidden-tab refreshChanges requests")
	}
}

func TestTaskDetailContent_FileChangesListenersRebindAndCleanup(t *testing.T) {
	task := &models.Task{
		ID:        "task-4",
		Title:     "Task",
		ProjectID: "project-1",
		Status:    models.StatusRunning,
		Category:  models.CategoryActive,
	}

	var buf bytes.Buffer
	err := TaskDetailContent(task, nil, nil, nil, nil, nil, "changes", nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "if (window._taskDetailFileChangesHandlers) {") {
		t.Fatal("expected previous task-detail file-change handlers to be removed before rebinding")
	}
	if !strings.Contains(output, "window._taskDetailFileChangesHandlers = {") {
		t.Fatal("expected task-detail file-change handlers to be stored for future cleanup")
	}
	if !strings.Contains(output, "if (target.id === 'main-content' || target.id === 'task-detail-content') {") {
		t.Fatal("expected beforeSwap handler to stop file-change SSE on navigation/content replacement")
	}
	if !strings.Contains(output, "window.addEventListener('beforeunload', _taskDetailBeforeUnloadHandler);") {
		t.Fatal("expected beforeunload cleanup binding for file-change SSE")
	}
}

func TestTaskDetailContent_ThreadTabLazyLoadsOnDemand(t *testing.T) {
	task := &models.Task{
		ID:        "task-thread-1",
		Title:     "Task",
		ProjectID: "project-1",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}

	var buf bytes.Buffer
	err := TaskDetailContent(task, nil, nil, nil, nil, nil, "details", nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Thread loads on demand when you open this tab.") {
		t.Fatal("expected thread placeholder copy for inactive tab")
	}
	if strings.Contains(output, "id=\"task-thread-view\"") {
		t.Fatal("did not expect eager thread view render for inactive thread tab")
	}
	if !strings.Contains(output, "function _loadThreadContent(taskId) {") {
		t.Fatal("expected on-demand thread loader helper")
	}
	if !strings.Contains(output, "htmx.ajax('GET', '/tasks/' + taskId + '/thread'") {
		t.Fatal("expected thread loader to fetch /tasks/:id/thread via HTMX")
	}
	if !strings.Contains(output, "if (tabName === 'chat') {") || !strings.Contains(output, "_loadThreadContent(taskId).then(function() {") {
		t.Fatal("expected chat tab switch to trigger thread lazy load")
	}
}

// TestTaskDetailContent_DiffUpdateUsesPreSwapFingerprint is a regression test for the
// Changes tab viewport-jump bug. The old code used htmx.ajax() which swapped the DOM
// before the fingerprint check could fire, causing full DOM remounts every 2 seconds
// during live updates. The fix uses fetch() + offscreen fingerprint comparison so the
// DOM is only touched when content actually changes.
func TestTaskDetailContent_DiffUpdateUsesPreSwapFingerprint(t *testing.T) {
	task := &models.Task{
		ID:        "task-fp-1",
		Title:     "Task",
		ProjectID: "project-1",
		Status:    models.StatusRunning,
		Category:  models.CategoryActive,
	}

	var buf bytes.Buffer
	err := TaskDetailContent(task, nil, nil, nil, nil, nil, "changes", nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	output := buf.String()

	// Must use fetch() for diff updates, NOT htmx.ajax() — fetch allows pre-swap
	// fingerprint comparison in a detached DOM element.
	if !strings.Contains(output, "fetch('/tasks/' + taskId + '/changes'") {
		t.Fatal("expected _updateDiffViewer to use fetch() for pre-swap fingerprint comparison")
	}

	// Must NOT use htmx.ajax for live diff updates inside _updateDiffViewer.
	// htmx.ajax is fine for initial tab-switch loads and refreshChanges triggers.
	// Check that the function body between "function _updateDiffViewer" and its
	// closing does not call htmx.ajax (excluding comments).
	if idx := strings.Index(output, "function _updateDiffViewer"); idx >= 0 {
		end := idx + 2500
		if end > len(output) {
			end = len(output)
		}
		fnBody := output[idx:end]
		// Remove comment lines before checking for htmx.ajax calls
		var codeLines []string
		for _, line := range strings.Split(fnBody, "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "//") {
				codeLines = append(codeLines, line)
			}
		}
		codeOnly := strings.Join(codeLines, "\n")
		if strings.Contains(codeOnly, "htmx.ajax") {
			t.Fatal("_updateDiffViewer must NOT use htmx.ajax (causes DOM swap before fingerprint check); use fetch() instead")
		}
	}

	// Must compute fingerprint on offscreen element before touching live DOM.
	if !strings.Contains(output, "var offscreen = document.createElement") {
		t.Fatal("expected offscreen DOM element for pre-swap fingerprint computation")
	}

	// Must skip DOM mutation entirely when fingerprint matches.
	if !strings.Contains(output, "// Diff unchanged") {
		t.Fatal("expected early return path when diff fingerprint is unchanged")
	}

	// Must use requestAnimationFrame for post-swap UI state restoration.
	if !strings.Contains(output, "requestAnimationFrame(function()") {
		t.Fatal("expected requestAnimationFrame for post-swap state restoration")
	}
}

func TestTaskDetailContent_ThreadAutoLoadsWhenChatTabInitiallyActive(t *testing.T) {
	task := &models.Task{
		ID:        "task-thread-2",
		Title:     "Task",
		ProjectID: "project-1",
		Status:    models.StatusRunning,
		Category:  models.CategoryActive,
	}

	var buf bytes.Buffer
	err := TaskDetailContent(task, nil, nil, nil, nil, nil, "chat", nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Thread is loading...") {
		t.Fatal("expected loading placeholder when chat tab is initially active")
	}
	if !strings.Contains(output, "if (_isChatTabActive()) {") {
		t.Fatal("expected initial-load handler to detect active chat tab")
	}
	if !strings.Contains(output, "_loadThreadContent(taskId).then(function() {") {
		t.Fatal("expected initial-load handler to lazy load thread content")
	}
}

func TestTaskDetailContent_RunAtFieldsClickablePickerAffordance(t *testing.T) {
	task := &models.Task{
		ID:        "task-schedule-1",
		Title:     "Task",
		ProjectID: "project-1",
		Status:    models.StatusPending,
		Category:  models.CategoryScheduled,
	}
	runAt := time.Date(2026, 1, 20, 15, 30, 0, 0, time.UTC)
	nextRun := runAt
	schedules := []models.Schedule{{
		ID:             "schedule-1",
		TaskID:         task.ID,
		RunAt:          runAt,
		NextRun:        &nextRun,
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
	}}

	var buf bytes.Buffer
	err := TaskDetailContent(task, nil, schedules, nil, nil, nil, "schedules", nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	output := buf.String()
	if strings.Count(output, `data-run-at-picker-container`) < 2 {
		t.Fatal("expected run-at picker containers for both add and edit schedule forms")
	}
	if strings.Count(output, `onclick="openScheduleRunAtPicker(this, event)"`) < 2 {
		t.Fatal("expected run-at container click handlers for both add and edit forms")
	}
	if strings.Count(output, `data-run-at-picker`) < 2 {
		t.Fatal("expected run-at picker input hooks for both add and edit forms")
	}
	if strings.Count(output, `input-sm cursor-pointer`) < 2 {
		t.Fatal("expected pointer cursor affordance on run-at datetime inputs in add/edit forms")
	}
	if !strings.Contains(output, `if (event && event.target && !event.target.closest('input[data-run-at-picker]')) return;`) {
		t.Fatal("expected run-at picker open behavior to be scoped to clicks on the datetime input")
	}
	if !strings.Contains(output, `function openScheduleRunAtPicker(container, event)`) {
		t.Fatal("expected shared run-at picker open helper in task detail script")
	}
	if !strings.Contains(output, `if (typeof pickerInput.showPicker === 'function')`) {
		t.Fatal("expected showPicker-based open behavior with fallback focus")
	}
}
