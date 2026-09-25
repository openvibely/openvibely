package handler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestMergeTaskToolMergesAbortsConflictsAndKeepsRepoUsable(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	h.SetWorktreeService(service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo))
	ctx := context.Background()

	repoDir := createHandlerTestGitRepo(t)
	project := &models.Project{Name: "Chat Merge Project", RepoPath: repoDir}
	require.NoError(t, h.projectSvc.Create(ctx, project))
	params := streamingResponseParams{ProjectID: project.ID}

	mkTask := func(title, file, content string) *models.Task {
		task := &models.Task{
			ProjectID: project.ID, Title: title, Prompt: "p", Category: models.CategoryCompleted,
			Status: models.StatusCompleted, MergeTargetBranch: "main", MergeStatus: models.MergeStatusPending,
		}
		require.NoError(t, h.taskRepo.Create(ctx, task))
		wtPath, branch, err := h.worktreeSvc.SetupWorktree(ctx, task, repoDir)
		require.NoError(t, err)
		require.NoError(t, h.taskRepo.UpdateWorktreeInfo(ctx, task.ID, wtPath, branch))
		require.NoError(t, os.WriteFile(filepath.Join(wtPath, file), []byte(content), 0644))
		runGit(t, wtPath, "add", ".")
		runGit(t, wtPath, "commit", "-m", title)
		return task
	}
	call := func(args map[string]string) mergeTaskToolResult {
		t.Helper()
		input, _ := json.Marshal(args)
		out, err := h.executeMergeTaskTool(ctx, params, input)
		require.NoError(t, err)
		var result mergeTaskToolResult
		require.NoError(t, json.Unmarshal([]byte(out), &result))
		return result
	}

	clean := mkTask("clean change", "clean.txt", "clean\n")
	conflicting := mkTask("conflicting change", "README.md", "# From task\n")
	later := mkTask("later change", "later.txt", "later\n")

	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# From main\n"), 0644))
	runGit(t, repoDir, "commit", "-am", "main edit")
	mainBefore := runGit(t, repoDir, "rev-parse", "main")

	result := call(map[string]string{"task_id": conflicting.ID})
	require.Equal(t, "conflict", result.Result, result.Message)
	require.False(t, result.OK)
	require.Contains(t, result.ConflictFiles, "README.md")
	require.False(t, service.HasActiveMerge(repoDir), "conflicted chat merge must be aborted")
	require.Equal(t, mainBefore, runGit(t, repoDir, "rev-parse", "main"))
	stored, err := h.taskRepo.GetByID(ctx, conflicting.ID)
	require.NoError(t, err)
	require.Equal(t, models.MergeStatusConflict, stored.MergeStatus)

	result = call(map[string]string{"task_id": conflicting.ID, "action": "rebase"})
	require.Equal(t, "conflict", result.Result, result.Message)

	result = call(map[string]string{"title": "clean change"})
	require.Equal(t, "merged", result.Result, result.Message)
	require.True(t, result.OK)
	require.Equal(t, "merge", result.Action)
	require.NotEmpty(t, result.MergeCommit)
	_, err = os.Stat(filepath.Join(repoDir, "clean.txt"))
	require.NoError(t, err)

	result = call(map[string]string{"task_id": clean.ID})
	require.Equal(t, "already_merged", result.Result, result.Message)

	result = call(map[string]string{"task_id": later.ID, "action": "rebase"})
	require.Equal(t, "rebased", result.Result, result.Message)
	result = call(map[string]string{"task_id": later.ID, "action": "fast_forward"})
	require.Equal(t, "merged", result.Result, result.Message)

	_, err = h.executeMergeTaskTool(ctx, params, json.RawMessage(`{"task_id":"`+clean.ID+`","action":"push"}`))
	require.Error(t, err)
	_, err = h.executeMergeTaskTool(ctx, streamingResponseParams{ProjectID: "other-project"}, json.RawMessage(`{"task_id":"`+later.ID+`"}`))
	require.Error(t, err)
}
