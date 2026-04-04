package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestTaskChangesWorktreeContent_RendersCreatePRButtonWhenMissingPR(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeStatus: models.MergeStatusPending}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, nil, true).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Create PR") {
		t.Fatal("expected Create PR button")
	}
}

func TestTaskChangesWorktreeContent_RendersViewPRLinkWhenPRExists(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeStatus: models.MergeStatusPending}
	pr := &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: "https://github.com/openvibely/openvibely/pull/42", PRState: "open"}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, pr, true).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "View PR #42") {
		t.Fatal("expected View PR link")
	}
	if strings.Contains(out, "Create PR") {
		t.Fatal("did not expect Create PR button when PR exists")
	}
}

func TestTaskChangesWorktreeContent_HidesMergeOptionsWhenFlagDisabled(t *testing.T) {
	task := &models.Task{ID: "task-1", WorktreeBranch: "task/feature", MergeStatus: models.MergeStatusPending}
	var buf bytes.Buffer
	if err := TaskChangesWorktreeContent("diff --git", task, nil, nil, nil, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "Merge to") {
		t.Fatal("did not expect merge dropdown when feature flag is disabled")
	}
	if strings.Contains(out, "/worktree/merge") {
		t.Fatal("did not expect merge endpoint actions when feature flag is disabled")
	}
}
