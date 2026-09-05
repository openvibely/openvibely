package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

func stringPtr(s string) *string { return &s }

func TestTaskDetailContentIncludesAuthoritativeDynamicPageTitle(t *testing.T) {
	task := &models.Task{ID: "task-title", ProjectID: "project-title", Title: "Investigate <title> & history"}
	var buf bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "details", nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render task detail content: %v", err)
	}

	html := buf.String()
	if strings.Contains(html, "history.pushState") {
		t.Fatal("task detail must use centralized HTMX-managed navigation instead of manual history.pushState")
	}
	if !strings.Contains(html, `data-openvibely-page-title="Investigate &lt;title&gt; &amp; history - OpenVibely"`) {
		t.Fatalf("task detail fragment missing escaped authoritative title marker: %s", html)
	}
}

func TestTaskDetailBreadcrumbSupportsAutomationOrigin(t *testing.T) {
	task := &models.Task{ID: "task-automation-origin", ProjectID: "project-origin", Title: "Automation task"}
	var buf bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "details", nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render task detail: %v", err)
	}
	body := buf.String()
	for _, want := range []string{
		`id="task-automations-back-btn"`,
		`data-automations-url="/automations?project_id=project-origin"`,
		`id="task-automations-separator"`,
		`data-automation-url-base="/automations/"`,
		`from === 'automation'`,
		`params.get('automation_id')`,
		`params.get('automation_name')`,
		`encodeURIComponent(automationID)`,
		`automationsBtn.hidden = false`,
		`automationsSeparator.hidden = false`,
		`data-breadcrumb-selector`,
		`Switch Task`,
		`/breadcrumb-selectors/tasks?project_id=project-origin&amp;current_id=task-automation-origin`,
		`name="tab" value="details" data-breadcrumb-selector-context`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected Automation-origin breadcrumb support to contain %q", want)
		}
	}
}

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
			metrics := models.TaskExecutionMetrics{}

			var buf bytes.Buffer
			err := TaskDetailMetrics(task, metrics, nil, "").Render(context.Background(), &buf)
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
		ID:        "task-1",
		Title:     "Task",
		ProjectID: "project-1",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}
	reviewComments := []models.ReviewComment{{ID: "c1", CommentText: "x"}}

	var buf bytes.Buffer
	err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "changes", reviewComments).Render(context.Background(), &buf)
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

func TestTaskDetailContent_TabsRemainScrollableOnMobile(t *testing.T) {
	task := &models.Task{
		ID:        "task-tabs",
		Title:     "Task",
		ProjectID: "project-1",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}

	var buf bytes.Buffer
	err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "details", nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, `role="tablist" class="tabs tabs-bordered tabs-sm mb-6 flex-shrink-0 w-full overflow-x-auto flex-nowrap"`) {
		t.Fatal("expected task detail tabs to scroll horizontally instead of clipping on mobile")
	}
	for _, label := range []string{"Details", "Thread", "Changes", "Schedules", "Chaining", "Attachments", "Lifecycle"} {
		if !strings.Contains(output, ">"+label+"</a>") {
			t.Fatalf("expected %s tab to remain rendered", label)
		}
	}
}

func TestTaskDetailContent_ReactivatesFileChangesSSEWhenTaskBecomesActive(t *testing.T) {
	task := &models.Task{
		ID:        "task-2",
		Title:     "Task",
		ProjectID: "project-1",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}

	var buf bytes.Buffer
	err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "changes", nil).Render(context.Background(), &buf)
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
	err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "details", nil).Render(context.Background(), &buf)
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
	err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "changes", nil).Render(context.Background(), &buf)
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

func TestTaskDetailContent_LifecycleTabFillsRemainingHeight(t *testing.T) {
	task := &models.Task{
		ID:        "task-lifecycle-layout",
		Title:     "Lifecycle layout",
		ProjectID: "project-1",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}

	var buf bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "lifecycle", nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}

	output := buf.String()
	for _, required := range []string{
		`id="tab-lifecycle" class="task-tab-panel flex-1 flex flex-col min-h-0"`,
		`class="card bg-base-100 shadow-sm border border-base-300 flex-1 min-h-0"`,
		`class="card-body flex flex-col min-h-0"`,
		`data-lifecycle-description class="text-sm opacity-70 mb-3 flex-shrink-0"`,
		`id="lifecycle-activity-scroll"`,
		`class="flex-1 min-h-0 overflow-y-auto pr-1"`,
	} {
		if !strings.Contains(output, required) {
			t.Fatalf("expected viewport-filling lifecycle layout to contain %q", required)
		}
	}
	if strings.Contains(output, `max-height: 32rem`) || strings.Contains(output, `max-h-128`) {
		t.Fatal("lifecycle scrollport must not retain the fixed 32rem height cap")
	}
}

func TestTaskDetailContent_DetailsTabRendersScrollableMatchedSectionCards(t *testing.T) {
	task := &models.Task{
		ID:                "task-layout-1",
		Title:             "Task",
		ProjectID:         "project-1",
		Status:            models.StatusCompleted,
		Category:          models.CategoryCompleted,
		Prompt:            "Do the thing",
		AgentID:           stringPtr("model-1"),
		AgentDefinitionID: stringPtr("agent-1"),
		Tag:               models.TagFeature,
		WorktreeBranch:    "task/layout",
		WorktreePath:      "/tmp/worktree",
		MergeTargetBranch: "main",
	}
	goal := &models.TaskGoal{
		GoalID:    "goal-1",
		TaskID:    task.ID,
		Status:    models.TaskGoalStatusActive,
		Objective: "Keep the layout clean",
	}

	modelsList := []models.LLMConfig{{ID: "model-1", Name: "Model One"}}
	agentsList := []models.Agent{{ID: "agent-1", Name: "Agent One", Enabled: true, SelectableAsPrimary: true}}

	var buf bytes.Buffer
	err := TaskDetailContent(task, goal, nil, nil, modelsList, agentsList, nil, "details", nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, `id="task-detail-view" class="flex-1 overflow-y-auto min-h-0 pr-1"`) {
		t.Fatal("expected Details view to be the scroll container")
	}
	sectionIDs := []string{`id="task-prompt-panel"`, `id="task-goal-panel"`, `id="worktree-info-panel"`}
	lastIndex := -1
	for _, id := range sectionIDs {
		idx := strings.Index(output, id)
		if idx == -1 {
			t.Fatalf("expected details section card %s", id)
		}
		if idx <= lastIndex {
			t.Fatalf("expected details sections in Prompt, Goal, Git Worktree order; %s rendered out of order", id)
		}
		lastIndex = idx
	}
	if got := strings.Count(output, `class="card bg-base-200/50 border border-base-300 mb-4"`); got < 3 {
		t.Fatalf("expected prompt, goal, and worktree cards to share section styling, got %d matching cards", got)
	}
	if !strings.Contains(output, `class="textarea textarea-bordered textarea-sm w-full min-h-32 h-auto cursor-default whitespace-pre-wrap overflow-x-auto font-sans text-sm leading-relaxed"`) {
		t.Fatal("expected prompt content box to match the goal textarea styling")
	}
	if strings.Contains(output, `flex-1 min-h-0 flex flex-col mb-6`) {
		t.Fatal("prompt should not render as an uncontained flex filler")
	}
	if strings.Contains(output, `<pre class="p-4 bg-base-100/60 border border-base-300 rounded-lg text-sm`) {
		t.Fatal("prompt should not render with the old monospace pre styling")
	}
	for _, required := range []string{"Model One", "Agent One", "Feature", `name="goal"`, `name="goal_active"`, `name="auto_merge"`, `>active</span>`} {
		if !strings.Contains(output, required) {
			t.Fatalf("expected task details/edit markup to include %q", required)
		}
	}
	if strings.Contains(output, "Active: true") || strings.Contains(output, "Active: false") {
		t.Fatal("goal panel should use the status pill instead of redundant boolean active text")
	}
	viewStart := strings.Index(output, `id="task-detail-view"`)
	editStart := strings.Index(output, `id="task-detail-edit"`)
	if viewStart == -1 || editStart == -1 || editStart <= viewStart {
		t.Fatal("expected task detail view before edit form")
	}
	viewOnly := output[viewStart:editStart]
	for _, forbidden := range []string{"Add goal", "Pause", "Resume", "Clear", "Auto-merge on completion", `name="auto_merge"`} {
		if strings.Contains(viewOnly, forbidden) {
			t.Fatalf("read-only details view should not include edit/configuration control %q", forbidden)
		}
	}
}

func TestTaskDetailMetrics_ShowsMissingTagModelAndAgentClearly(t *testing.T) {
	task := &models.Task{
		ID:       "task-missing-summary",
		Title:    "Task",
		Status:   models.StatusPending,
		Category: models.CategoryBacklog,
		Priority: 2,
	}

	var buf bytes.Buffer
	if err := TaskDetailMetrics(task, models.TaskExecutionMetrics{}, nil, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	output := buf.String()
	for _, required := range []string{"Tag:", "None", "Model:", "Default model", "Agent:", "No agent", "Priority:", "Normal"} {
		if !strings.Contains(output, required) {
			t.Fatalf("expected metrics to include %q, got: %s", required, output)
		}
	}
	for _, requiredClass := range []string{
		`<span class="ml-2 badge badge-sm badge-outline">backlog</span>`,
		`<span class="ml-2 badge badge-sm badge-outline opacity-70">None</span>`,
	} {
		if !strings.Contains(output, requiredClass) {
			t.Fatalf("expected neutral metadata badge class %q, got: %s", requiredClass, output)
		}
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
	err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "details", nil).Render(context.Background(), &buf)
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
	if !strings.Contains(output, "function _loadThreadContent(taskId, forceReload, expectedExecId) {") {
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
	err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "changes", nil).Render(context.Background(), &buf)
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

	// Must re-process manually inserted HTML so HTMX actions rendered by the Changes
	// partial (including merge buttons) are bound after fetch()+innerHTML swaps.
	if !strings.Contains(output, "htmx.process(changesContent)") {
		t.Fatal("expected _updateDiffViewer to call htmx.process after manual innerHTML swap")
	}

	// Must use requestAnimationFrame for post-swap UI state restoration.
	if !strings.Contains(output, "requestAnimationFrame(function()") {
		t.Fatal("expected requestAnimationFrame for post-swap state restoration")
	}
	if strings.Contains(output, "// Restore diff view mode (inline/split).\t\t\t\t\t\t\tif (viewMode") ||
		strings.Contains(output, "// Restore diff view mode (inline/split).							if (viewMode") {
		t.Fatal("expected diff view restoration if-statement to stay off the line comment")
	}
	if !strings.Contains(output, "// Restore diff view mode (inline/split) without saving during refresh restore.\n") ||
		!strings.Contains(output, "if ((viewMode === 'inline' || viewMode === 'split') && typeof switchDiffView === 'function') {") ||
		!strings.Contains(output, "switchDiffView(viewMode, false)") {
		t.Fatal("expected syntactically valid diff view restoration block")
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
	err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "chat", nil).Render(context.Background(), &buf)
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

func TestTaskDetailContent_ThreadStartEventsUseFragmentAppendNotFullReload(t *testing.T) {
	task := &models.Task{
		ID:        "task-thread-run-race",
		Title:     "Task",
		ProjectID: "project-1",
		Status:    models.StatusRunning,
		Category:  models.CategoryActive,
	}

	var buf bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "chat", nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	output := buf.String()

	required := []string{
		"function _closeTaskThreadEventSourcesForRefresh() {",
		"function _loadThreadContent(taskId, forceReload, expectedExecId) {",
		"if (!threadContent || !taskId || threadContent.dataset.taskId !== taskId) return Promise.resolve(false);",
		"if (!forceReload && threadContent.dataset.loaded === 'true' && (!expectedExecId || _threadHasExecution(expectedExecId))) return Promise.resolve(true);",
		"if (forceReload) _closeTaskThreadEventSourcesForRefresh();",
		"target: '#thread-content[data-task-id=",
		"taskId.replace",
		"if (!updated || updated.dataset.taskId !== taskId) return false;",
		"if (window.htmx && typeof htmx.process === 'function') htmx.process(updated);",
		"if (expectedExecId && !_threadHasExecution(expectedExecId)) return false;",
		"function _refreshActiveThreadContent(taskId, forceReload, expectedExecId, attempt) {",
		"if (!taskId || !_isChatTabActive() || !threadContent || threadContent.dataset.taskId !== taskId) return;",
		"_loadThreadContent(taskId, forceReload, expectedExecId).then(function(loaded) {",
		"if (!currentThreadContent || currentThreadContent.dataset.taskId !== taskId) return;",
		"setTimeout(function() { _refreshActiveThreadContent(taskId, true, expectedExecId, attempt + 1); }, 150 * (attempt + 1));",
		"if (data.type === 'task_thread_execution_started' || data.type === 'task_thread_input_applied') {",
		"TaskThreadLiveEventsScript owns these events",
		"without replacing #thread-content or closing the fresh stream",
		"if (data.type === 'task_status_changed') {",
		"var activeStatuses = { pending: true, queued: true, running: true };",
		"_refreshActiveThreadContent(data.task_id, true, data.exec_id || '', 0);",
		"window.addEventListener('sse-live-connected', _taskDetailLiveConnectedHandler);",
	}
	for _, s := range required {
		if !strings.Contains(output, s) {
			t.Fatalf("expected task detail script to contain %q", s)
		}
	}
}

// TestTaskDetailContent_LiveConnectedHandler_SkipsInitialConnect verifies that
// _taskDetailLiveConnectedHandler checks detail.reconnected and returns early for
// the initial SSE connection event. Without this guard the handler would trigger a
// redundant full thread reload on page load (the page-init path already handles it).
func TestTaskDetailContent_LiveConnectedHandler_SkipsInitialConnect(t *testing.T) {
	task := &models.Task{
		ID:        "task-live-initial",
		Title:     "Task",
		ProjectID: "project-1",
		Status:    models.StatusRunning,
		Category:  models.CategoryActive,
	}

	var buf bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "chat", nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	output := buf.String()

	if !strings.Contains(output, "var _taskDetailLiveConnectedHandler = function(event) {") {
		t.Fatal("expected _taskDetailLiveConnectedHandler to accept an event parameter")
	}
	if !strings.Contains(output, "if (!detail.reconnected) return;") {
		t.Fatal("expected _taskDetailLiveConnectedHandler to return early when detail.reconnected is false")
	}
}

func TestTaskDetailContent_LiveConnectedHandler_ReconcilesWithoutForcedThreadReload(t *testing.T) {
	task := &models.Task{
		ID:        "task-live-stable",
		Title:     "Task",
		ProjectID: "project-1",
		Status:    models.StatusRunning,
		Category:  models.CategoryActive,
	}

	var buf bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "chat", nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	output := buf.String()

	if strings.Contains(output, "_refreshActiveThreadContent(taskId, true);") {
		t.Fatal("focus reconnect must not blindly replace the loaded task thread")
	}
	for _, required := range []string{
		"if (!taskId || !_isChatTabActive()) return;",
		"threadContent.dataset.loaded !== 'true'",
		"window.reconcileTaskThreadState(taskId)",
	} {
		if !strings.Contains(output, required) {
			t.Fatalf("expected state-aware reconnect hook %q", required)
		}
	}
}

// TestTaskDetailContent_LiveConnectedHandler_PreservesPendingAttachmentOnReconnect
// verifies that _taskDetailLiveConnectedHandler skips the full #thread-content reload
// when the task-thread composer has a pending attachment upload session. Without this
// guard, returning to a tab after switching away triggers an innerHTML swap that wipes
// the user's unsent attachment previews and session ID.
func TestTaskDetailContent_LiveConnectedHandler_PreservesPendingAttachmentOnReconnect(t *testing.T) {
	task := &models.Task{
		ID:        "task-attach-reconnect",
		Title:     "Task",
		ProjectID: "project-1",
		Status:    models.StatusRunning,
		Category:  models.CategoryActive,
	}

	var buf bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "chat", nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	output := buf.String()

	// The handler must read the session input by the stable element ID so it
	// can detect a pending upload and bail out before replacing the composer.
	if !strings.Contains(output, "var sessionInput = document.getElementById('task-thread-form-session-id');") {
		t.Fatal("expected _taskDetailLiveConnectedHandler to read attachment session input by ID")
	}
	if !strings.Contains(output, "if (sessionInput && sessionInput.value) return;") {
		t.Fatal("expected _taskDetailLiveConnectedHandler to return early when a pending attachment session is active")
	}
}

// TestTaskDetailContent_TaskEventHandler_DoesNotFullReloadOnExecutionStarted
// verifies that the page-level task detail listener does not also reload the
// whole task-thread panel for task_thread_execution_started / task_thread_input_applied.
// The scoped TaskThreadLiveEventsScript owns those events and appends the
// authoritative execution fragment without closing per-execution streams.
func TestTaskDetailContent_TaskEventHandler_DoesNotFullReloadOnExecutionStarted(t *testing.T) {
	task := &models.Task{
		ID:        "task-exec-start-no-reload",
		Title:     "Task",
		ProjectID: "project-1",
		Status:    models.StatusRunning,
		Category:  models.CategoryActive,
	}

	var buf bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "chat", nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	output := buf.String()

	branchStart := strings.Index(output, "if (data.type === 'task_thread_execution_started' || data.type === 'task_thread_input_applied') {")
	if branchStart < 0 {
		t.Fatal("expected execution start/applied branch")
	}
	branchEnd := strings.Index(output[branchStart:], "if (data.type === 'task_status_changed') {")
	if branchEnd < 0 {
		t.Fatal("expected status branch after execution start/applied branch")
	}
	branch := output[branchStart : branchStart+branchEnd]
	if !strings.Contains(branch, "TaskThreadLiveEventsScript owns these events") {
		t.Fatal("expected execution start/applied branch to defer to TaskThreadLiveEventsScript")
	}
	if strings.Contains(branch, "_refreshActiveThreadContent") || strings.Contains(branch, "task-thread-form-session-id") {
		t.Fatal("execution start/applied branch must not full-reload thread content or depend on attachment-session guards")
	}
}

// TestTaskDetailContent_TaskEventHandler_PreservesPendingAttachmentOnStatusChanged
// verifies that the task_status_changed → active-status branch of
// _taskDetailTaskEventHandler also guards the full thread reload when a pending
// attachment upload session is active, covering the analogous live-event path.
func TestTaskDetailContent_TaskEventHandler_PreservesPendingAttachmentOnStatusChanged(t *testing.T) {
	task := &models.Task{
		ID:        "task-attach-status-change",
		Title:     "Task",
		ProjectID: "project-1",
		Status:    models.StatusRunning,
		Category:  models.CategoryActive,
	}

	var buf bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "chat", nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	output := buf.String()

	if !strings.Contains(output, "var sessionInputSC = document.getElementById('task-thread-form-session-id');") {
		t.Fatal("expected task_status_changed handler to read attachment session input by ID")
	}
	if !strings.Contains(output, "if (!sessionInputSC || !sessionInputSC.value) {") {
		t.Fatal("expected task_status_changed handler to guard _refreshActiveThreadContent behind attachment session check")
	}
}

func TestTaskDetailActions_RunButtonLoadsThreadThroughRaceSafeLoader(t *testing.T) {
	task := &models.Task{
		ID:        "task-run-button",
		Title:     "Task",
		ProjectID: "project-1",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}

	var buf bytes.Buffer
	if err := TaskDetailActions(task).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	output := buf.String()

	if !strings.Contains(output, "if(event.detail.successful) { window._openTaskThreadAfterRun && window._openTaskThreadAfterRun('task-run-button'); }") {
		t.Fatalf("expected Run button to delegate to the race-safe thread opener, got: %s", output)
	}
	if strings.Contains(output, "htmx.ajax('GET', '/tasks/task-run-button?tab=chat'") {
		t.Fatal("Run button should not bypass the race-safe thread loader with a raw detail-content refresh")
	}
}

func TestTaskDetailContent_ThreadTabRestoresPerTaskScrollState(t *testing.T) {
	task := &models.Task{
		ID:        "task-thread-scroll-1",
		Title:     "Task",
		ProjectID: "project-1",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}

	var buf bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "details", nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	output := buf.String()

	required := []string{
		"window._taskThreadScrollStates = window._taskThreadScrollStates || {};",
		"return taskId ? 'task-thread-scroll-' + taskId : '';",
		"function _restoreThreadScrollOrBottom(taskId, forceBottom) {",
		"chatMessages.scrollTop = state.userScrolledUp ? (state.scrollTop || 0) : chatMessages.scrollHeight;",
		"userScrolledUp: userScrolledUp, pinned: !userScrolledUp",
		"if (_isChatTabActive()) {",
		"_saveTaskThreadScrollState();",
		"_restoreThreadScrollOrBottom(taskId, false);",
	}
	for _, r := range required {
		if !strings.Contains(output, r) {
			t.Fatalf("expected task detail thread tab scroll code to include %q", r)
		}
	}

	if strings.Contains(output, "_scrollThreadToBottom(false)") {
		t.Fatal("thread tab switching should restore per-task scroll state instead of blindly bottom-aligning")
	}
}

func TestTaskDetailContent_DeleteButtonCarriesScheduleReturnContext(t *testing.T) {
	task := &models.Task{
		ID:        "task-schedule-delete",
		Title:     "Scheduled Task",
		ProjectID: "project-1",
		Status:    models.StatusPending,
		Category:  models.CategoryScheduled,
	}

	var buf bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "details", nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	output := buf.String()

	if !strings.Contains(output, `id="delete-task-confirm-button"`) {
		t.Fatal("expected delete confirmation button to have a stable id for return-context wiring")
	}
	if !strings.Contains(output, `hx-delete="/tasks/task-schedule-delete?redirect=list"`) {
		t.Fatal("expected default delete flow to keep redirecting to tasks list")
	}
	if !strings.Contains(output, `data-schedule-delete-url="/tasks/task-schedule-delete?redirect=list&amp;return_to=schedule"`) {
		t.Fatal("expected safe schedule return token on delete button")
	}
	if !strings.Contains(output, `if (params.get('from') !== 'schedule') return;`) {
		t.Fatal("expected script to activate schedule delete return only from schedule context")
	}
	buttonIndex := strings.Index(output, `id="delete-task-confirm-button"`)
	scriptIndex := strings.Index(output, `deleteBtn.setAttribute('hx-delete', deleteBtn.dataset.scheduleDeleteUrl)`)
	if scriptIndex == -1 {
		t.Fatal("expected script to update the HTMX delete endpoint for schedule-origin task details")
	}
	if scriptIndex < buttonIndex {
		t.Fatal("expected schedule delete return script to run after the delete button exists")
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
	err := TaskDetailContent(task, nil, nil, schedules, nil, nil, nil, "schedules", nil).Render(context.Background(), &buf)
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
func TestTaskDetailContent_AgentSelectorAllowsNoAgentSelection(t *testing.T) {
	task := &models.Task{ID: "task1", ProjectID: "project1", Title: "Task", Status: models.StatusPending, Category: models.CategoryActive}
	agentDefs := []models.Agent{{ID: "agent1", Name: "Reviewer", Key: "reviewer", Model: "inherit", Enabled: true, SelectableAsPrimary: true}}

	var buf bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, agentDefs, nil, "details", nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render task detail: %v", err)
	}
	body := buf.String()
	if strings.Contains(body, "Auto (router selects each run)") {
		t.Fatal("agent selector must not offer automatic agent routing")
	}
	if strings.Contains(body, `name="agent_definition_id" class="select select-bordered" required`) {
		t.Fatalf("agent selector should allow an intentionally unassigned task, body=%s", body)
	}
	if !strings.Contains(body, `option value="" selected>No Agent</option>`) {
		t.Fatalf("agent selector should preserve the no-agent option, body=%s", body)
	}
	if strings.Contains(body, `name="agent_definition_present"`) {
		t.Fatalf("edit form should not need a sentinel for the no-agent option, body=%s", body)
	}
}

func TestTaskDetailContent_ScheduleAgentSelectorsHydratePersistedAssignment(t *testing.T) {
	selectedID := "agent-selected"
	task := &models.Task{
		ID:                "task-scheduled-agent",
		ProjectID:         "project-1",
		Title:             "Scheduled Agent Task",
		Status:            models.StatusPending,
		Category:          models.CategoryScheduled,
		AgentDefinitionID: &selectedID,
	}
	runAt := time.Now().Add(time.Hour).UTC()
	schedules := []models.Schedule{{
		ID:             "schedule-agent-1",
		TaskID:         task.ID,
		RunAt:          runAt,
		NextRun:        &runAt,
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
	}}
	agentDefs := []models.Agent{
		{ID: selectedID, Name: "Selected Runner", Model: "inherit", Scope: models.AgentScopeProject, ProjectID: task.ProjectID, Enabled: true, SelectableAsPrimary: true},
		{ID: "protected", Name: "Protected Maintenance", Model: "inherit", Scope: models.AgentScopeGlobal, Enabled: true, SelectableAsPrimary: false},
	}

	var buf bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, schedules, nil, agentDefs, nil, "schedules", nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render task detail: %v", err)
	}
	body := buf.String()
	editStart := strings.Index(body, `id="schedule-edit-schedule-agent-1"`)
	if editStart == -1 {
		t.Fatal("expected schedule edit form")
	}
	editBody := body[editStart:]
	for _, want := range []string{
		`name="schedule_agent_definition_present" value="1"`,
		`name="agent_definition_id"`,
		`>Primary Agent</span>`,
		`value="agent-selected" selected`,
		`action="/schedules/schedule-agent-1?project_id=project-1"`,
		`name="_method" value="PUT"`,
		`/schedules/schedule-agent-1?project_id=project-1`,
		`grid grid-cols-1 gap-4 mb-4 sm:grid-cols-2`,
	} {
		if !strings.Contains(editBody, want) {
			t.Fatalf("expected schedule edit form to contain %q", want)
		}
	}
	if strings.Contains(editBody, "Protected Maintenance") {
		t.Fatal("schedule Agent choices must exclude agents that are not selectable as primary")
	}
}

func TestTaskDetailContent_ScheduleAgentSelectorsSupportNoAgent(t *testing.T) {
	task := &models.Task{ID: "task-no-agent", ProjectID: "project-1", Title: "No Agent Task", Status: models.StatusPending, Category: models.CategoryScheduled}
	runAt := time.Now().Add(time.Hour).UTC()
	schedules := []models.Schedule{{ID: "schedule-no-agent", TaskID: task.ID, RunAt: runAt, NextRun: &runAt, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true}}
	agentDefs := []models.Agent{{ID: "runner", Name: "Runner", Model: "inherit", Scope: models.AgentScopeGlobal, Enabled: true, SelectableAsPrimary: true}}

	var buf bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, schedules, nil, agentDefs, nil, "schedules", nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render task detail: %v", err)
	}
	body := buf.String()
	if strings.Count(body, `>No Agent</option>`) < 3 {
		t.Fatalf("expected no-agent option in task, add-schedule, and edit-schedule selectors")
	}
	if strings.Count(body, `option value="" selected>No Agent</option>`) < 3 {
		t.Fatalf("expected server-rendered no-agent selection in all schedule-related selectors")
	}
	if !strings.Contains(body, `/tasks/task-no-agent/schedule?project_id=project-1`) {
		t.Fatal("expected add-schedule form to preserve project scope")
	}
}

func TestTaskDetailContent_ScheduleEditDoesNotOfferOrClearProtectedAgent(t *testing.T) {
	protectedID := "protected-agent"
	task := &models.Task{ID: "task-protected-agent", ProjectID: "project-1", Title: "Protected Agent Task", Status: models.StatusPending, Category: models.CategoryScheduled, AgentDefinitionID: &protectedID}
	runAt := time.Now().Add(time.Hour).UTC()
	schedules := []models.Schedule{{ID: "schedule-protected-agent", TaskID: task.ID, RunAt: runAt, NextRun: &runAt, RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}}
	agentDefs := []models.Agent{{ID: protectedID, Name: "Protected Maintenance", Model: "inherit", Scope: models.AgentScopeGlobal, Enabled: true, SelectableAsPrimary: false}}

	var buf bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, schedules, nil, agentDefs, nil, "schedules", nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render task detail: %v", err)
	}
	body := buf.String()
	editStart := strings.Index(body, `id="schedule-edit-schedule-protected-agent"`)
	if editStart == -1 {
		t.Fatal("expected protected schedule edit form")
	}
	editBody := body[editStart:]
	if strings.Contains(editBody, "Protected Maintenance") || strings.Contains(editBody, `name="schedule_agent_definition_present"`) {
		t.Fatal("protected Agent must not be exposed or overwritten by the schedule edit form")
	}
}

// TestTaskDetailContent_ScheduleEnabledState verifies the task detail schedule
// card renders the correct controls and badges based on Schedule.Enabled.
func TestTaskDetailContent_ScheduleEnabledState(t *testing.T) {
	runAt := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)
	nextRun := runAt

	tests := []struct {
		name            string
		enabled         bool
		wantBadge       string // text that MUST appear
		wantNoBadge     string // text that must NOT appear
		wantButton      string // button label that MUST appear
		wantNoButton    string // button label that must NOT appear
		wantLineThrough bool   // expect line-through on next-run timestamp
	}{
		{
			name:            "disabled schedule shows Disabled badge and Resume button",
			enabled:         false,
			wantBadge:       "Disabled",
			wantNoBadge:     "",
			wantButton:      "Resume",
			wantNoButton:    "Pause",
			wantLineThrough: true,
		},
		{
			name:            "enabled schedule shows no Disabled badge and Pause button",
			enabled:         true,
			wantBadge:       "",
			wantNoBadge:     "Disabled",
			wantButton:      "Pause",
			wantNoButton:    "Resume",
			wantLineThrough: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			task := &models.Task{
				ID:        "task-sched-1",
				Title:     "My Task",
				ProjectID: "p1",
				Category:  models.CategoryScheduled,
				Status:    models.StatusPending,
			}
			schedules := []models.Schedule{{
				ID:             "sched-1",
				TaskID:         task.ID,
				RunAt:          runAt,
				NextRun:        &nextRun,
				RepeatType:     models.RepeatDaily,
				RepeatInterval: 1,
				Enabled:        tc.enabled,
			}}

			var buf bytes.Buffer
			err := TaskDetailContent(task, nil, nil, schedules, nil, nil, nil, "schedules", nil).Render(context.Background(), &buf)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			out := buf.String()

			if tc.wantBadge != "" && !strings.Contains(out, tc.wantBadge) {
				t.Errorf("expected %q badge, not found in output", tc.wantBadge)
			}
			if tc.wantNoBadge != "" && strings.Contains(out, tc.wantNoBadge) {
				t.Errorf("expected %q badge to be absent, but found in output", tc.wantNoBadge)
			}
			if !strings.Contains(out, tc.wantButton) {
				t.Errorf("expected %q button, not found in output", tc.wantButton)
			}
			if strings.Contains(out, ">"+tc.wantNoButton+"<") {
				t.Errorf("expected %q button to be absent, but found in output", tc.wantNoButton)
			}
			if tc.wantLineThrough && !strings.Contains(out, "line-through") {
				t.Error("expected line-through CSS class for disabled next-run timestamp")
			}
			if !tc.wantLineThrough && strings.Contains(out, "line-through") {
				t.Error("expected no line-through CSS class for enabled schedule")
			}
		})
	}
}

func TestTaskDetailScheduleSurfacesDefaultAndHydrateClearContext(t *testing.T) {
	task := &models.Task{ID: "task-schedule-context", ProjectID: "default", Title: "Scheduled context"}
	schedules := []models.Schedule{
		{ID: "clear", TaskID: task.ID, RunAt: time.Now(), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true, ClearContextOnStart: true},
		{ID: "keep", TaskID: task.ID, RunAt: time.Now(), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true},
	}
	var buf bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, schedules, nil, nil, nil, "schedules", nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := buf.String()
	if strings.Count(html, `name="clear_context_on_start" value="true"`) != 3 {
		t.Fatalf("expected add and two schedule edit controls, got %d", strings.Count(html, `name="clear_context_on_start" value="true"`))
	}
	if strings.Contains(html, `name="clear_context_schedule_ids"`) {
		t.Fatal("Task Details edit form must not render schedule context controls")
	}
	clearCardStart := strings.Index(html, `id="schedule-card-clear"`)
	keepCardStart := strings.Index(html, `id="schedule-card-keep"`)
	if clearCardStart < 0 || keepCardStart < 0 {
		t.Fatal("expected both schedule cards")
	}
	clearCard := html[clearCardStart:keepCardStart]
	keepCard := html[keepCardStart:]
	checkedInput := `name="clear_context_on_start" value="true" class="checkbox checkbox-sm" checked`
	if !strings.Contains(clearCard, checkedInput) {
		t.Fatal("enabled persisted schedule must render checked")
	}
	if strings.Contains(keepCard, checkedInput) {
		t.Fatal("disabled persisted schedule must render unchecked")
	}
}
