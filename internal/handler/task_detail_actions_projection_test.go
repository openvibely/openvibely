package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestHandlerGetTaskDetailActionsUsesCompactTaskMetadata(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	h, e, _ := setupTestHandlerForDB(t, db)
	ctx := context.Background()

	const (
		promptBytes = 4 * 1024 * 1024
		configBytes = 1 * 1024 * 1024
	)
	task := createTask(t, h, "default", "Large Detail Actions Task", func(task *models.Task) {
		task.Prompt = strings.Repeat("p", promptBytes)
	})
	chainConfig := strings.Repeat("c", configBytes)
	swarmConfig := strings.Repeat("s", configBytes)
	if _, err := db.ExecContext(ctx, `UPDATE tasks
		SET chain_config = ?, swarm_config = ?, worktree_path = ?, worktree_branch = ?,
			merge_target_branch = ?, merge_status = ?, base_branch = ?, base_commit_sha = ?, lineage_depth = ?
		WHERE id = ?`,
		chainConfig, swarmConfig, "/private/worktree", "task/large-detail-actions", "main",
		models.MergeStatusPending, "main", "deadbeef", 42, task.ID); err != nil {
		t.Fatalf("seed large detail-only task fields: %v", err)
	}

	render := func(t *testing.T, status models.TaskStatus) *httptest.ResponseRecorder {
		t.Helper()
		if _, err := db.ExecContext(ctx, `UPDATE tasks SET status = ? WHERE id = ?`, status, task.ID); err != nil {
			t.Fatalf("set status %s: %v", status, err)
		}
		req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/detail-actions", nil)
		rec := httptest.NewRecorder()
		counter.Reset()
		counter.SetEnabled(true)
		e.ServeHTTP(rec, req)
		counter.SetEnabled(false)
		assertTaskDetailActionsProjectionQuery(t, counter)
		return rec
	}

	for _, status := range []models.TaskStatus{models.StatusPending, models.StatusCompleted, models.StatusFailed, models.StatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			rec := render(t, status)
			assertCode(t, rec, http.StatusOK)
			assertContains(t, rec, `id="task-detail-actions"`)
			assertContains(t, rec, `hx-post="/tasks/`+task.ID+`/run"`)
			assertContains(t, rec, "Edit")
			assertNotContains(t, rec, "disabled")
		})
	}

	t.Run("queued", func(t *testing.T) {
		rec := render(t, models.StatusQueued)
		assertCode(t, rec, http.StatusOK)
		assertContains(t, rec, "disabled")
		assertNotContains(t, rec, `hx-post="/tasks/`+task.ID+`/run"`)
		assertContains(t, rec, "Edit")
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

	full, err := repository.NewTaskRepo(db, nil).GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if len(full.Prompt) != promptBytes || full.ChainConfig != chainConfig || full.SwarmConfig != swarmConfig || full.WorktreePath != "/private/worktree" || full.MergeStatus != models.MergeStatusPending || full.BaseBranch != "main" {
		t.Fatalf("full task read did not retain detail fields: prompt=%d chain=%d swarm=%d worktree=%q merge=%q base=%q", len(full.Prompt), len(full.ChainConfig), len(full.SwarmConfig), full.WorktreePath, full.MergeStatus, full.BaseBranch)
	}
}

func assertTaskDetailActionsProjectionQuery(t *testing.T, counter *testutil.SQLStatementCounter) {
	t.Helper()
	var taskStatements []string
	for _, statement := range counter.Statements() {
		if strings.Contains(strings.ToLower(statement), "from tasks") {
			taskStatements = append(taskStatements, statement)
		}
	}
	if len(taskStatements) != 1 {
		t.Fatalf("task statements = %#v, want one detail-actions metadata query", taskStatements)
	}

	query := strings.ToLower(strings.Join(strings.Fields(taskStatements[0]), " "))
	projection, _, found := strings.Cut(query, " from tasks")
	if !found {
		t.Fatalf("task metadata query missing FROM tasks: %s", taskStatements[0])
	}
	if projection != "select id, status" {
		t.Fatalf("task metadata projection = %q, want only id and status", projection)
	}
	for _, forbidden := range []string{
		"prompt", "chain_config", "swarm_config", "worktree_path", "worktree_branch",
		"auto_merge", "merge_target_branch", "merge_status", "base_branch", "base_commit_sha", "lineage_depth",
	} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("task metadata query selected forbidden field %q: %s", forbidden, taskStatements[0])
		}
	}
}
