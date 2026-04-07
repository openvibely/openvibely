package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestTaskChangesWorktreeContent_RendersCreatePRInGitHubSection(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeStatus: models.MergeStatusPending}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, nil, true).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Create PR") {
		t.Fatal("expected Create PR item in dropdown")
	}
	if !strings.Contains(out, "GitHub") {
		t.Fatal("expected GitHub section header in dropdown")
	}
}

func TestTaskChangesWorktreeContent_RendersViewPRInGitHubSection(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeStatus: models.MergeStatusPending}
	pr := &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: "https://github.com/openvibely/openvibely/pull/42", PRState: "open"}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, pr, true).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "View PR #42") {
		t.Fatal("expected View PR link in dropdown")
	}
	if strings.Contains(out, "Create PR") {
		t.Fatal("did not expect Create PR when PR exists")
	}
	if !strings.Contains(out, "GitHub") {
		t.Fatal("expected GitHub section header in dropdown")
	}
}

func TestTaskChangesWorktreeContent_HidesMergeOptionsWhenFlagDisabled(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeStatus: models.MergeStatusPending}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, nil, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	// Local merge section should not appear
	if strings.Contains(out, "/worktree/merge") {
		t.Fatal("did not expect merge endpoint actions when feature flag is disabled")
	}
	// GitHub section should still render with Create PR
	if !strings.Contains(out, "GitHub") {
		t.Fatal("expected GitHub section header even when merge options disabled")
	}
	if !strings.Contains(out, "Create PR") {
		t.Fatal("expected Create PR in GitHub section when merge options disabled")
	}
}

func TestTaskChangesWorktreeContent_LocalAndGitHubSections(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeStatus: models.MergeStatusPending}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, nil, true).Render(context.Background(), &buf); err != nil {
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
	if !strings.Contains(out, "Create PR") {
		t.Fatal("expected Create PR in GitHub section")
	}
}

func TestTaskChangesWorktreeContent_MergedStatusHidesLocalSection(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeStatus: models.MergeStatusMerged}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, nil, true).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	// When merged, local merge options should not appear even with flag on
	if strings.Contains(out, "/worktree/merge") {
		t.Fatal("did not expect merge endpoint actions when already merged")
	}
	// GitHub section should still render
	if !strings.Contains(out, "GitHub") {
		t.Fatal("expected GitHub section header when already merged")
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
}
