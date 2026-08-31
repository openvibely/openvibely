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
		`hx-indicator="#create-pr-indicator"`,
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

func TestTaskChangesWorktreeContent_FileStatsStayContained(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeStatus: models.MergeStatusPending}
	fileStats := []service.WorktreeFileStat{
		{Path: "web/templates/pages/this/is/a/very/deep/path/with/an/extremely-long-file-name-that-should-not-overflow-the-task-changes-container.templ", Status: "M"},
	}

	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, fileStats, nil, nil, false, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `<ul class="text-xs space-y-1 min-w-0 max-w-full overflow-hidden">`) {
		t.Fatal("expected file stats list to constrain overflowing rows")
	}
	if !strings.Contains(out, `<li class="flex items-center gap-2 min-w-0 max-w-full overflow-hidden">`) {
		t.Fatal("expected file stat row to stay within its container")
	}
	if !strings.Contains(out, `<span class="font-mono min-w-0 flex-1 truncate" title="web/templates/pages/this/is/a/very/deep/path/with/an/extremely-long-file-name-that-should-not-overflow-the-task-changes-container.templ">`) {
		t.Fatal("expected long file path to truncate inside the available row width")
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
	if !strings.Contains(out, `<div class="flex flex-wrap items-start justify-between gap-2 mb-4 min-w-0 max-w-full">`) {
		t.Fatal("expected worktree header row to wrap within the card without clipping the actions dropdown")
	}
	if !strings.Contains(out, `<p class="text-sm opacity-60 min-w-0 max-w-full break-words [overflow-wrap:anywhere]">`) {
		t.Fatal("expected branch summary to break long labels instead of widening the card")
	}
	if !strings.Contains(out, `<code class="bg-base-200 px-1.5 py-0.5 rounded text-xs break-all">task/this-is-a-very-long-worktree-branch-name-that-should-not-overflow-the-task-changes-header-on-mobile</code>`) {
		t.Fatal("expected worktree branch label to break inside the header")
	}
	if !strings.Contains(out, `<code class="bg-base-200 px-1.5 py-0.5 rounded text-xs break-all">main/this-is-a-very-long-target-branch-name-that-should-not-overflow-the-task-changes-header-on-mobile</code>`) {
		t.Fatal("expected target branch label to break inside the header")
	}
}

func TestTaskChangesWorktreeContent_ActionsDropdownLayersAboveDiffHeaders(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeStatus: models.MergeStatusPending}

	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, nil, false, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `<div class="dropdown dropdown-end relative z-[60] flex-shrink-0" id="changes-actions-dropdown">`) {
		t.Fatal("expected Actions dropdown to establish a stacking context above sticky diff headers")
	}
	if !strings.Contains(out, `<ul tabindex="0" class="dropdown-content z-[100] menu p-2 shadow bg-base-100 rounded-box w-52 max-w-[calc(100vw-2rem)] border border-base-300">`) {
		t.Fatal("expected Actions menu content to layer above diff content and stay viewport-contained")
	}
	legacyActionsSnippet := `<div class="dropdown dropdown-end flex-shrink-0" id="changes-actions-dropdown"><div tabindex="0" role="button" class="btn btn-primary btn-sm whitespace-nowrap">Actions <svg xmlns="http://www.w3.org/2000/svg" class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"></path></svg></div><ul tabindex="0" class="dropdown-content z-[1] menu`
	if strings.Contains(out, legacyActionsSnippet) {
		t.Fatal("Actions dropdown must not use the previous low z-index that allowed sticky diff headers to cover it")
	}
}

func TestTaskChangesWorktreeContent_RendersCreatePRInGitHubSection(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeStatus: models.MergeStatusPending}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, nil, false, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Create PR") {
		t.Fatal("expected Create PR item in dropdown")
	}
	if !strings.Contains(out, "GitHub") {
		t.Fatal("expected GitHub section header in dropdown")
	}
	if !strings.Contains(out, `hx-indicator="#create-pr-indicator"`) {
		t.Fatal("expected Create PR action to include htmx indicator slot for consistent indentation")
	}
	if !strings.Contains(out, `id="create-pr-indicator"`) {
		t.Fatal("expected Create PR indicator element in dropdown action")
	}
}

func TestTaskChangesWorktreeContent_RendersViewPRInGitHubSection(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeStatus: models.MergeStatusPending}
	pr := &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: "https://github.com/openvibely/openvibely/pull/42", PRState: "open"}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, pr, false, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "View PR #42") {
		t.Fatal("expected View PR link in dropdown")
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
	if strings.Contains(out, "Create PR") {
		t.Fatal("did not expect Create PR when PR exists")
	}
	if !strings.Contains(out, "GitHub") {
		t.Fatal("expected GitHub section header in dropdown")
	}
	if !strings.Contains(out, `<span class="htmx-indicator"><span class="loading loading-spinner loading-xs"></span></span>`) {
		t.Fatal("expected View PR action to include indicator-width slot for consistent indentation")
	}
}

func TestTaskChangesWorktreeContent_LocalAndGitHubSections(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeStatus: models.MergeStatusPending}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, nil, false, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Local") {
		t.Fatal("expected Local section header in dropdown")
	}
	if !strings.Contains(out, "GitHub") {
		t.Fatal("expected GitHub section header in dropdown")
	}
	if !strings.Contains(out, "Merge commit") {
		t.Fatal("expected Merge commit option in Local section")
	}
	if !strings.Contains(out, "Fast-forward only") {
		t.Fatal("expected Fast-forward only option in Local section")
	}
	if !strings.Contains(out, "Squash merge") {
		t.Fatal("expected Squash merge option in Local section")
	}
	if !strings.Contains(out, `hx-target="#changes-content"`) {
		t.Fatal("expected Changes-tab local merge actions to target the visible changes content")
	}
	if !strings.Contains(out, `type="button"`) {
		t.Fatal("expected Changes-tab local merge actions to be explicit buttons so clicks cannot fall back to form submission")
	}
	if !strings.Contains(out, "Create PR") {
		t.Fatal("expected Create PR in GitHub section")
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
	if !strings.Contains(out, `hx-disabled-elt="#changes-actions-dropdown button"`) {
		t.Fatal("expected Rebase action to disable while request is in flight")
	}
}

func TestTaskChangesWorktreeContent_RebaseHiddenWhenUnavailable(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeStatus: models.MergeStatusPending}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, nil, false, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	if strings.Contains(buf.String(), "/worktree/rebase") {
		t.Fatal("did not expect Rebase action when task branch is not behind target")
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
	// GitHub section should still render
	if !strings.Contains(out, "GitHub") {
		t.Fatal("expected GitHub section header when already merged")
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
	for _, want := range []string{"Merge commit", "Fast-forward only", "Squash merge", `hx-disabled-elt="#changes-actions-dropdown button"`} {
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
	// GitHub section should still render (Create PR / View PR remains valid).
	if !strings.Contains(out, "GitHub") {
		t.Fatal("expected GitHub section header even when branch is already merged")
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
