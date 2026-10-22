package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestRunningExecutionsDiscovery(t *testing.T) {
	h, e, configs := setupTestHandler(t)
	agent := createAgent(t, configs)
	p1 := createProject(t, h, "One")
	p2 := createProject(t, h, "Two")
	one := createTask(t, h, p1.ID, "Thread")
	two := createTask(t, h, p2.ID, "Chat", func(task *models.Task) { task.Category = models.CategoryChat })
	running := createExec(t, h, one.ID, agent.ID, func(ex *models.Execution) { ex.Status = models.ExecRunning; ex.PromptSent = "private prompt" })
	queued := createExec(t, h, two.ID, agent.ID, func(ex *models.Execution) { ex.Status = models.ExecQueued })
	completed := createExec(t, h, one.ID, agent.ID, func(ex *models.Execution) { ex.Status = models.ExecCompleted })
	require.NoError(t, h.execRepo.Complete(t.Context(), completed.ID, models.ExecCompleted, "private output", "", 0, 0))
	req := httptest.NewRequest(http.MethodGet, "/events/running-executions", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var items []models.LiveExecution
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &items))
	require.ElementsMatch(t, []models.LiveExecution{
		{ID: running.ID, TaskID: one.ID, ProjectID: p1.ID, Category: one.Category, StartedAt: running.StartedAt.UTC(), HistoryOrder: running.HistoryOrder},
		{ID: queued.ID, TaskID: two.ID, ProjectID: p2.ID, Category: models.CategoryChat, StartedAt: queued.StartedAt.UTC(), HistoryOrder: queued.HistoryOrder},
	}, items)
	require.NotContains(t, rec.Body.String(), "private")
	require.Positive(t, running.HistoryOrder)
	require.Greater(t, queued.HistoryOrder, running.HistoryOrder)
	require.NoError(t, h.execRepo.Complete(t.Context(), running.ID, models.ExecCompleted, "final", "", 0, 0))
	items, err := h.execRepo.ListLiveExecutions(t.Context())
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, queued.ID, items[0].ID)
}
