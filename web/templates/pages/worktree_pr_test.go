package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
)

func TestTaskChangesWorktreeContent_CreatePRIsDirectActionWithoutEndpointModal(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeStatus: models.MergeStatusPending}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, nil, false, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		`hx-post="/tasks/task-1/worktree/pull-request"`,
		`hx-target="#changes-content"`,
		`data-changes-pr-transport`,
		`hx-disabled-elt="[data-changes-split] button"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected direct Create PR action to contain %q", want)
		}
	}
	for _, unwanted := range []string{
		`id="create-pr-modal-task-1"`,
		`name="github_api_endpoint"`,
		`toggleTaskChangesPRModal`,
	} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("Create PR must not introduce endpoint modal content %q", unwanted)
		}
	}
}

func TestTaskChangesWorktreeContent_DoesNotDuplicateFileList(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeStatus: models.MergeStatusPending}
	fileStats := []service.WorktreeFileStat{
		{Path: "web/templates/pages/this/is/a/very/deep/path/with/an/extremely-long-file-name-that-should-not-overflow-the-task-changes-container.templ", Status: "M"},
	}

	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, fileStats, nil, nil, false, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, fileStats[0].Path) {
		t.Fatal("worktree summary must not duplicate the diff explorer's file list")
	}
}

func TestTaskChangesWorktreeContent_HeaderLabelsStayContained(t *testing.T) {
	task := &models.Task{
		ID:                "task-1",
		WorktreeBranch:    "task/this-is-a-very-long-worktree-branch-name-that-should-not-overflow-the-task-changes-header-on-mobile",
		MergeTargetBranch: "main/this-is-a-very-long-target-branch-name-that-should-not-overflow-the-task-changes-header-on-mobile",
		MergeStatus:       models.MergeStatusPending,
	}

	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, nil, false, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `<div class="relative flex flex-wrap items-start justify-between gap-2 mb-2 min-w-0 max-w-full">`) {
		t.Fatal("expected worktree header row to wrap within the page without clipping the action settings")
	}
	if !strings.Contains(out, `<p class="text-sm opacity-60 min-w-0 max-w-full break-words [overflow-wrap:anywhere]">`) {
		t.Fatal("expected branch summary to break long labels instead of widening the page")
	}
	if !strings.Contains(out, `<code class="bg-base-200 px-1.5 py-0.5 rounded text-xs break-all">task/this-is-a-very-long-worktree-branch-name-that-should-not-overflow-the-task-changes-header-on-mobile</code>`) {
		t.Fatal("expected worktree branch label to break inside the header")
	}
	if !strings.Contains(out, `<code class="bg-base-200 px-1.5 py-0.5 rounded text-xs break-all">main/this-is-a-very-long-target-branch-name-that-should-not-overflow-the-task-changes-header-on-mobile</code>`) {
		t.Fatal("expected target branch label to break inside the header")
	}
}

func TestTaskChangesWorktreeContent_SettingsUseTopLayerPopover(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeStatus: models.MergeStatusPending}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, nil, false, false).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		`data-changes-settings-toggle popovertarget="changes-split-settings"`,
		`aria-label="Action settings" aria-expanded="false"`,
		`id="changes-split-settings" data-menu-content hidden popover="manual"`,
		`style="position:fixed;inset:auto;margin:0;"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing split settings popover markup %q", want)
		}
	}
	// Actual layering and viewport placement are exercised by
	// TestBrowserFunctional_WorktreeActionsAboveDiffHeader.
	if strings.Contains(out, `id="changes-actions-dropdown"`) {
		t.Fatal("ordinary actions should use the split settings, not the legacy menu")
	}
}

func TestTaskChangesWorktreeContent_RendersPRSettingsAndDirectAction(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeStatus: models.MergeStatusPending}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, nil, false, false).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		`data-changes-primary aria-disabled="true" aria-busy="true"`,
		`data-changes-mode="pr" aria-pressed="false">Pull request</button>`,
		`data-changes-repo`, `data-changes-branch`,
		`data-changes-pr-transport hx-post="/tasks/task-1/worktree/pull-request"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing PR action control %q", want)
		}
	}
	if strings.Contains(out, `data-linked-pr-action href=`) {
		t.Fatal("unlinked task must not render a View PR link")
	}
}

func TestTaskChangesWorktreeContent_RendersLinkedPRAction(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeStatus: models.MergeStatusPending}
	pr := &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: "https://github.com/openvibely/openvibely/pull/42", PRState: "open"}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, pr, false, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "View PR #42") {
		t.Fatal("expected View PR link in split button")
	}
	// The View PR action must point at the absolute GitHub PR URL and use
	// target="_blank" so the desktop bridge (in base layout) routes the click
	// to the system browser instead of relying on Wails WebView new-window
	// navigation, which silently drops the click.
	if !strings.Contains(out, `href="https://github.com/openvibely/openvibely/pull/42"`) {
		t.Fatal("expected View PR anchor to use absolute GitHub PR URL")
	}
	if !strings.Contains(out, `target="_blank"`) {
		t.Fatal("expected View PR anchor to use target=\"_blank\" so the desktop bridge can intercept it")
	}
	if strings.Contains(out, `data-changes-pr-transport hx-post=`) {
		t.Fatal("linked task must not render a Create PR transport")
	}
	for _, want := range []string{`data-linked-pr="true"`, `data-linked-pr-action href=`, `rel="noopener noreferrer"`, `data-changes-primary aria-disabled="true" aria-busy="true" hidden`, `data-changes-settings-toggle`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing linked PR control %q", want)
		}
	}
}

func TestTaskChangesWorktreeContent_LocalAndPRSettings(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeStatus: models.MergeStatusPending}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, nil, false, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	for _, want := range []string{`data-changes-mode="local" aria-pressed="false">Local merge</button>`, `data-changes-mode="pr" aria-pressed="false">Pull request</button>`, `data-changes-local-branch`, `data-changes-method`, `data-changes-pr-fields`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing action settings %q", want)
		}
	}
	if !strings.Contains(out, "Merge commit") {
		t.Fatal("expected Merge commit option in merge method selector")
	}
	if !strings.Contains(out, "Fast-forward only") {
		t.Fatal("expected Fast-forward only option in merge method selector")
	}
	if !strings.Contains(out, "Squash merge") {
		t.Fatal("expected Squash merge option in merge method selector")
	}
	if !strings.Contains(out, `hx-target="#changes-content"`) {
		t.Fatal("expected Changes-tab local merge actions to target the visible changes content")
	}
	if !strings.Contains(out, `type="button"`) {
		t.Fatal("expected Changes-tab local merge actions to be explicit buttons so clicks cannot fall back to form submission")
	}
	if !strings.Contains(out, `data-changes-pr-transport hx-post=`) {
		t.Fatal("expected PR creation transport")
	}
}

func TestTaskWorktreeMergeActionsShareMetadataAcrossWorktreeAndChanges(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeTargetBranch: "develop", MergeStatus: models.MergeStatusPending}

	var worktreeBuf bytes.Buffer
	if err := WorktreeInfoPanel(task, nil).Render(context.Background(), &worktreeBuf); err != nil {
		t.Fatalf("render worktree panel: %v", err)
	}
	worktree := worktreeBuf.String()
	for _, want := range []string{
		`data-task-worktree-merge-action`,
		`data-merge-type="merge"`,
		`data-merge-type="ff"`,
		`data-merge-type="squash"`,
		`data-merge-label="Merge commit"`,
		`data-merge-label="Fast-forward only"`,
		`data-merge-label="Squash merge"`,
		`data-merge-endpoint="merge"`,
		`hx-post="/tasks/task-1/worktree/merge"`,
		`hx-target="#worktree-info-panel"`,
		`Merge to develop`,
	} {
		if !strings.Contains(worktree, want) {
			t.Fatalf("expected Worktree action metadata %q, body=%s", want, worktree)
		}
	}
	if strings.Contains(worktree, `data-merge-type="rebase"`) {
		t.Fatalf("Worktree panel must preserve its existing lack of a Rebase action, body=%s", worktree)
	}
	if !strings.Contains(worktree, `class="text-xs" data-task-worktree-merge-action`) {
		t.Fatalf("Worktree actions must preserve their compact typography, body=%s", worktree)
	}

	var changesBuf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, nil, false, true).Render(context.Background(), &changesBuf); err != nil {
		t.Fatalf("render Changes content: %v", err)
	}
	changes := changesBuf.String()
	for _, want := range []string{
		`data-merge-type="merge"`,
		`data-merge-type="ff"`,
		`data-merge-type="rebase"`,
		`data-merge-type="squash"`,
		`data-merge-label="Rebase"`,
		`data-merge-endpoint="rebase"`,
		`hx-post="/tasks/task-1/worktree/rebase"`,
		`hx-target="#changes-content"`,
		`Rebase onto develop`,
		`merge_source`,
		`changes_tab`,
	} {
		if !strings.Contains(changes, want) {
			t.Fatalf("expected Changes action metadata %q, body=%s", want, changes)
		}
	}
	if strings.Contains(changes, `class="text-xs" data-task-worktree-merge-action`) {
		t.Fatalf("Changes actions must retain the menu's default typography, body=%s", changes)
	}
}
func TestTaskChangesWorktreeContent_RebaseOnlyWhenAvailable(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeStatus: models.MergeStatusPending}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, nil, false, true).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "/worktree/rebase") {
		t.Fatal("expected Rebase action when task branch is behind target")
	}
	if !strings.Contains(out, "Rebase onto main") {
		t.Fatal("expected Rebase action label to include target branch")
	}
	if !strings.Contains(out, `hx-disabled-elt="[data-changes-split] button"`) {
		t.Fatal("expected Rebase action to disable while request is in flight")
	}
}

func TestTaskChangesWorktreeContent_RebaseTransportAvailableForTargetChanges(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeStatus: models.MergeStatusPending}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, nil, false, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	if !strings.Contains(buf.String(), "/worktree/rebase") {
		t.Fatal("expected Rebase transport so another target can enable it")
	}
}

func TestTaskChangesWorktreeContent_MergedStatusWithoutDiffHidesLocalSection(t *testing.T) {
	task := &models.Task{
		ID:             "task-1",
		WorktreeBranch: "task/feature",
		MergeStatus:    models.MergeStatusMerged,
		Status:         models.StatusCompleted,
	}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("", task, nil, nil, nil, false, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	// When merged and no diff remains, local merge options should not appear.
	if strings.Contains(out, "/worktree/merge") {
		t.Fatal("did not expect merge endpoint actions when already merged")
	}
	// PR creation remains available while local actions are disabled.
	if !strings.Contains(out, `data-changes-pr-transport hx-post=`) {
		t.Fatal("expected PR creation transport when already merged")
	}
	if !strings.Contains(out, `data-changes-mode="local" aria-pressed="false">Local merge</button>`) {
		t.Fatal("merged task must allow viewing local settings")
	}
}

func TestTaskChangesWorktreeContent_ConflictStatusShowsRecoveryWithoutMergeActions(t *testing.T) {
	task := &models.Task{
		ID:             "task-1",
		WorktreeBranch: "task/feature",
		MergeStatus:    models.MergeStatusConflict,
		Status:         models.StatusCompleted,
	}

	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, nil, false, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "/worktree/merge") || strings.Contains(out, "Fast-forward only") || strings.Contains(out, "Squash merge") {
		t.Fatalf("expected ordinary merge actions hidden while conflict is active, body=%s", out)
	}
	for _, want := range []string{"Conflict recovery", "AI Resolve Conflicts", "Abort Merge", `data-merge-conflict-guidance`, `hx-vals='{"merge_source": "changes_tab"}'`} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected conflict recovery content %q, body=%s", want, out)
		}
	}
	if !strings.Contains(out, "GitHub") {
		t.Fatalf("expected GitHub section to remain available, body=%s", out)
	}
}

func TestTaskChangesWorktreeContent_FailedStatusShowsRetryActions(t *testing.T) {
	task := &models.Task{
		ID:             "task-1",
		WorktreeBranch: "task/feature",
		MergeStatus:    models.MergeStatusFailed,
		Status:         models.StatusCompleted,
	}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, nil, false, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Merge commit", "Fast-forward only", "Squash merge", `hx-disabled-elt="[data-changes-split] button"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected failed status retry content %q, body=%s", want, out)
		}
	}
	if strings.Contains(out, "Conflict recovery") {
		t.Fatalf("failed status must not show conflict recovery actions, body=%s", out)
	}
}

func TestTaskChangesWorktreeContent_FailedMergedStatusHidesLocalSection(t *testing.T) {
	task := &models.Task{
		ID:             "task-1",
		WorktreeBranch: "task/feature",
		MergeStatus:    models.MergeStatusMerged,
		Status:         models.StatusFailed,
	}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, nil, false, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "/worktree/merge") {
		t.Fatal("did not expect merge endpoint actions when merge_status is merged, even for failed task status")
	}
}

func TestWorktreeInfoPanel_LocalSectionHeader(t *testing.T) {
	task := &models.Task{
		ID:                "task-1",
		WorktreeBranch:    "task/feature",
		MergeTargetBranch: "main",
		MergeStatus:       models.MergeStatusPending,
	}
	var buf bytes.Buffer
	if err := WorktreeInfoPanel(task, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Local") {
		t.Fatal("expected Local section header in worktree info panel merge dropdown")
	}
	if !strings.Contains(out, "Merge commit") {
		t.Fatal("expected Merge commit option in worktree info panel")
	}
	if !strings.Contains(out, `hx-target="#worktree-info-panel"`) {
		t.Fatal("expected worktree panel merge actions to refresh the worktree panel")
	}
	if !strings.Contains(out, `type="button"`) {
		t.Fatal("expected worktree panel merge actions to be explicit buttons so clicks cannot fall back to form submission")
	}
}

// TestTaskChangesWorktreeContent_BranchAlreadyMergedHidesLocalSection ensures
// that when the task branch has already been merged into its target, local
// merge actions are suppressed even if `merge_status` is still stale (`pending`)
// and a preserved diff is being shown for context.
func TestTaskChangesWorktreeContent_BranchAlreadyMergedHidesLocalSection(t *testing.T) {
	task := &models.Task{
		ID:             "task-1",
		WorktreeBranch: "task/feature",
		MergeStatus:    models.MergeStatusPending, // stale
		Status:         models.StatusCompleted,
	}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git a/file.txt b/file.txt", task, nil, nil, nil, true, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "/worktree/merge") {
		t.Fatal("did not expect merge endpoint actions when branch is already merged")
	}
	if !strings.Contains(out, "file.txt") {
		t.Fatal("expected preserved diff to remain visible when local merge actions are hidden")
	}
	// PR controls remain available even when local integration is complete.
	if !strings.Contains(out, `data-changes-pr-transport hx-post=`) {
		t.Fatal("expected PR creation transport when branch is already merged")
	}
	if !strings.Contains(out, `data-changes-mode="local" aria-pressed="false">Local merge</button>`) {
		t.Fatal("already merged branch must allow viewing local settings")
	}
}

func TestTaskChangesWorktreeContent_MergedStatusWithDiffHidesLocalSection(t *testing.T) {
	task := &models.Task{
		ID:             "task-1",
		WorktreeBranch: "task/feature",
		MergeStatus:    models.MergeStatusMerged,
		Status:         models.StatusCompleted,
	}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git a/file.txt b/file.txt", task, nil, nil, nil, false, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "/worktree/merge") {
		t.Fatal("did not expect merge endpoint actions after app fast-forward merge, even when preserved diff exists")
	}
	if strings.Contains(out, "No changes detected") {
		t.Fatal("expected preserved diff to remain visible after merge")
	}
	if !strings.Contains(out, "file.txt") {
		t.Fatal("expected preserved diff file to remain visible after merge")
	}
}

func TestTaskChangesWorktreeContent_ToolbarActionOrder(t *testing.T) {
	task := &models.Task{ID: "task-toolbar", WorktreeBranch: "task/feature"}
	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n"
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent(diff, task, nil, nil, nil, false, false).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	last := -1
	for _, marker := range []string{`data-diff-toolbar`, `id="diff-btn-inline"`, `id="diff-btn-split"`, `id="review-toolbar"`, `data-changes-split data-task-id=`} {
		pos := strings.Index(out, marker)
		if pos <= last {
			t.Fatalf("missing or misplaced toolbar control %s", marker)
		}
		last = pos
	}
	for _, unwanted := range []string{"Expand All", "Collapse All", `class="card-body"`} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("unexpected toolbar chrome: %s", unwanted)
		}
	}
	if !strings.Contains(out, `class="btn btn-primary btn-sm join-item" data-changes-primary`) {
		t.Fatal("split primary action should retain its accent color")
	}
	buf.Reset()
	if err := TaskChangesWorktreeContent("", task, nil, nil, nil, false, false).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `data-changes-split data-task-id=`) {
		t.Fatal("actions must remain available with no diff")
	}
}

func TestTaskChangesWorktreeContent_RefreshesRunningStateWithUnchangedDiff(t *testing.T) {
	task := &models.Task{ID: "task-refresh", WorktreeBranch: "task/feature", MergeTargetBranch: "main", MergeStatus: models.MergeStatusMerged}
	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n"
	renderKey := func(status models.TaskStatus) string {
		t.Helper()
		task.Status = status
		var buf bytes.Buffer
		// Local merge remains unavailable across every execution state.
		if err := TaskChangesWorktreeContent(diff, task, nil, nil, nil, true, false).Render(context.Background(), &buf); err != nil {
			t.Fatal(err)
		}
		_, rest, ok := strings.Cut(buf.String(), `data-changes-metadata="`)
		if !ok {
			t.Fatal("missing toolbar refresh key")
		}
		key, _, ok := strings.Cut(rest, `"`)
		if !ok {
			t.Fatal("unterminated toolbar refresh key")
		}
		return key
	}
	stopped := renderKey(models.StatusCompleted)
	running := renderKey(models.StatusRunning)
	if stopped == running {
		t.Fatal("starting execution must refresh the toolbar despite unchanged diff and eligibility")
	}
	if finished := renderKey(models.StatusCompleted); finished == running || finished != stopped {
		t.Fatal("stopping execution must restore the non-running toolbar key")
	}
}
