package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestLoadBoundedTaskThreadExecutionsHandlesInvalidInputs(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	execRepo := repository.NewExecutionRepo(db)
	task := &models.Task{ID: "task-history-invalid"}

	for _, tt := range []struct {
		name     string
		execRepo *repository.ExecutionRepo
		task     *models.Task
		total    int
		offset   int
	}{
		{name: "nil repository", execRepo: nil, task: task, total: 1, offset: 0},
		{name: "nil task", execRepo: execRepo, task: nil, total: 1, offset: 0},
		{name: "empty history", execRepo: execRepo, task: task, total: 0, offset: 0},
		{name: "negative offset", execRepo: execRepo, task: task, total: 1, offset: -1},
		{name: "out of range offset", execRepo: execRepo, task: task, total: 1, offset: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LoadBoundedTaskThreadExecutions(ctx, tt.execRepo, tt.task, tt.total, tt.offset, func([]models.Execution) bool {
				t.Fatal("budget callback should not run for invalid or empty inputs")
				return false
			})
			require.NoError(t, err)
			require.Empty(t, got)
		})
	}
}

func TestLoadBoundedTaskThreadExecutionsCoversPagingBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name      string
		prefix    string
		actual    int
		total     int
		offset    int
		wantIDs   []string
		wantLens  []int
		wantPages int
	}{
		{name: "stale total empty batch", prefix: "history-empty", actual: 0, total: 10, offset: 0, wantIDs: []string{}, wantPages: 1},
		{name: "single short page", prefix: "history-short", actual: 3, total: 3, offset: 0, wantIDs: serviceThreadHistoryIDs("history-short", 0, 3), wantLens: []int{3}, wantPages: 1},
		{name: "exact one full page", prefix: "history-full", actual: 20, total: 20, offset: 0, wantIDs: serviceThreadHistoryIDs("history-full", 0, 20), wantLens: []int{20}, wantPages: 1},
		{name: "short final page after full page", prefix: "history-final-short", actual: 25, total: 25, offset: 0, wantIDs: serviceThreadHistoryIDs("history-final-short", 0, 25), wantLens: []int{20, 25}, wantPages: 2},
		{name: "offset boundary spans another page", prefix: "history-offset", actual: 45, total: 45, offset: 20, wantIDs: serviceThreadHistoryIDs("history-offset", 20, 45), wantLens: []int{20, 25}, wantPages: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			db, counter, execRepo, task := newServiceThreadHistoryFixture(t, tt.name)
			seedServiceThreadHistoryExecutions(t, ctx, db, execRepo, task.ID, tt.actual, tt.prefix)

			var callbackLens []int
			counter.Reset()
			counter.SetEnabled(true)
			got, err := LoadBoundedTaskThreadExecutions(ctx, execRepo, task, tt.total, tt.offset, func(loaded []models.Execution) bool {
				callbackLens = append(callbackLens, len(loaded))
				return false
			})
			counter.SetEnabled(false)

			require.NoError(t, err)
			require.Equal(t, tt.wantIDs, serviceThreadHistoryExecutionIDs(got))
			require.Equal(t, tt.wantLens, callbackLens)
			require.Len(t, serviceThreadHistoryExecutionQueries(counter.Statements()), tt.wantPages)
		})
	}
}

func TestLoadBoundedTaskThreadExecutionsStopsOnBudgetAfterBoundedPages(t *testing.T) {
	ctx := context.Background()
	db, counter, execRepo, task := newServiceThreadHistoryFixture(t, "budget stop")
	seedServiceThreadHistoryExecutions(t, ctx, db, execRepo, task.ID, 60, "history-budget")

	var callbackLens []int
	counter.Reset()
	counter.SetEnabled(true)
	got, err := LoadBoundedTaskThreadExecutions(ctx, execRepo, task, 60, 5, func(loaded []models.Execution) bool {
		callbackLens = append(callbackLens, len(loaded))
		return len(loaded) >= 40
	})
	counter.SetEnabled(false)

	require.NoError(t, err)
	require.Equal(t, serviceThreadHistoryIDs("history-budget", 5, 45), serviceThreadHistoryExecutionIDs(got))
	require.Equal(t, []int{20, 40}, callbackLens)
	require.Len(t, serviceThreadHistoryExecutionQueries(counter.Statements()), 2)
}

func newServiceThreadHistoryFixture(t *testing.T, name string) (*sql.DB, *testutil.SQLStatementCounter, *repository.ExecutionRepo, *models.Task) {
	t.Helper()
	db, counter := testutil.NewStatementCountingTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	project := &models.Project{Name: "Thread History " + name}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Thread History Task " + name,
		Category:  models.CategoryBacklog,
		Status:    models.StatusCompleted,
		Prompt:    "original prompt",
	}
	require.NoError(t, taskRepo.Create(ctx, task))
	return db, counter, execRepo, task
}

func seedServiceThreadHistoryExecutions(t *testing.T, ctx context.Context, db *sql.DB, execRepo *repository.ExecutionRepo, taskID string, count int, idPrefix string) {
	t.Helper()
	startedAt := "2026-01-02 03:04:05"
	for i := 0; i < count; i++ {
		exec := &models.Execution{
			ID:         fmt.Sprintf("%s-%02d", idPrefix, i),
			TaskID:     taskID,
			Status:     models.ExecRunning,
			PromptSent: fmt.Sprintf("prompt-%02d", i),
			IsFollowup: i > 0,
		}
		require.NoError(t, execRepo.Create(ctx, exec))
		_, err := db.ExecContext(ctx, `UPDATE executions SET started_at = ? WHERE id = ?`, startedAt, exec.ID)
		require.NoError(t, err)
		require.NoError(t, execRepo.Complete(ctx, exec.ID, models.ExecCompleted, fmt.Sprintf("output-%02d", i), "", 0, 0))
	}
}

func serviceThreadHistoryIDs(prefix string, start, end int) []string {
	ids := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		ids = append(ids, fmt.Sprintf("%s-%02d", prefix, i))
	}
	return ids
}

func serviceThreadHistoryExecutionIDs(executions []models.Execution) []string {
	ids := make([]string, 0, len(executions))
	for _, execution := range executions {
		ids = append(ids, execution.ID)
	}
	return ids
}

func serviceThreadHistoryExecutionQueries(statements []string) []string {
	var queries []string
	for _, statement := range statements {
		if strings.Contains(strings.ToLower(statement), "from executions") {
			queries = append(queries, statement)
		}
	}
	return queries
}
