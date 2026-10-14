package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestHandlerGetTaskDetailActionsUsesCompactTaskMetadata(t *testing.T) {
	h, e, _, db := setupTestHandlerWithStores(t, teststores.WithIOCounting())
	counter := db.IO
	ctx := context.Background()

	const (
		promptBytes = 4 * 1024 * 1024
		configBytes = 1 * 1024 * 1024
	)
	chainConfig := strings.Repeat("c", configBytes)
	swarmConfig := strings.Repeat("s", configBytes)
	task := createTask(t, h, "default", "Large Detail Actions Task", func(task *models.Task) {
		task.Prompt = strings.Repeat("p", promptBytes)
		task.ChainConfig = chainConfig
		task.SwarmConfig = swarmConfig
		task.WorktreePath = "/private/worktree"
		task.WorktreeBranch = "task/large-detail-actions"
		task.MergeTargetBranch = "main"
		task.MergeStatus = models.MergeStatusPending
		task.BaseBranch = "main"
		task.BaseCommitSHA = "deadbeef"
		task.LineageDepth = 42
	})

	render := func(t *testing.T, status models.TaskStatus) *httptest.ResponseRecorder {
		t.Helper()
		require.NoError(t, db.Fixtures.SetTaskStatus(ctx, task.ID, status))
		req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/detail-actions", nil)
		rec := httptest.NewRecorder()
		counter.Reset()
		counter.SetEnabled(true)
		e.ServeHTTP(rec, req)
		counter.SetEnabled(false)
		stats := counter.Snapshot()
		require.Equal(t, 1, stats.TaskReads)
		require.Equal(t, 1, stats.TaskActionReads)
		require.Less(t, stats.ReadTextBytes, 1024)
		return rec
	}

	for _, status := range []models.TaskStatus{models.StatusPending, models.StatusCompleted, models.StatusFailed, models.StatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			rec := render(t, status)
			assertCode(t, rec, http.StatusOK)
			assertContains(t, rec, `id="task-detail-actions"`)
			assertContains(t, rec, `hx-post="/tasks/`+task.ID+`/run"`)
			assertContains(t, rec, "Delete")
			assertNotContains(t, rec, "disabled")
		})
	}

	t.Run("queued", func(t *testing.T) {
		rec := render(t, models.StatusQueued)
		assertCode(t, rec, http.StatusOK)
		assertContains(t, rec, "disabled")
		assertNotContains(t, rec, `hx-post="/tasks/`+task.ID+`/run"`)
		assertContains(t, rec, "Delete")
	})

	t.Run("running", func(t *testing.T) {
		rec := render(t, models.StatusRunning)
		assertCode(t, rec, http.StatusOK)
		assertContains(t, rec, "disabled")
		assertNotContains(t, rec, `hx-post="/tasks/`+task.ID+`/run"`)
		assertNotContains(t, rec, "data-task-detail-edit")
	})

	t.Run("missing", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/tasks/missing/detail-actions", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		assertCode(t, rec, http.StatusNotFound)
	})

	full, err := db.Tasks.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if len(full.Prompt) != promptBytes || full.ChainConfig != chainConfig || full.SwarmConfig != swarmConfig || full.WorktreePath != "/private/worktree" || full.MergeStatus != models.MergeStatusPending || full.BaseBranch != "main" {
		t.Fatalf("full task read did not retain detail fields: prompt=%d chain=%d swarm=%d worktree=%q merge=%q base=%q", len(full.Prompt), len(full.ChainConfig), len(full.SwarmConfig), full.WorktreePath, full.MergeStatus, full.BaseBranch)
	}
}
