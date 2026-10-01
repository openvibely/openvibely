package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestTaskChainPanelSavesOnlyPanelAndPreservesConfiguration(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	task := &models.Task{ProjectID: "default", Title: "Chain panel", Category: models.CategoryBacklog, Prompt: "Parent"}
	require.NoError(t, setTaskPanelChainConfig(task, &models.ChainConfiguration{Trigger: "on_completion", ChildTitle: "Keep title", ChildPromptPrefix: "Keep instructions", ChildChainConfig: &models.ChainConfiguration{Enabled: true, Trigger: "on_completion"}}))
	require.NoError(t, h.taskSvc.Create(ctx, task))
	for _, action := range []string{"enable", "pause", "remove"} {
		form := url.Values{"chain_enabled": {"true"}, "chain_trigger": {"on_completion"}, "chain_child_category": {"backlog"}}
		if action == "pause" {
			form.Set("chain_enabled", "false")
		}
		if action == "remove" {
			form.Set("chain_remove", "true")
		}
		req := httptest.NewRequest(http.MethodPut, "/tasks/"+task.ID+"/chain?project_id=default&from=task-panel", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), `id="task-chain-panel"`)
		require.NotContains(t, rec.Body.String(), `id="task-detail-content"`)
		saved, err := h.taskRepo.GetByID(ctx, task.ID)
		require.NoError(t, err)
		config, err := saved.ParseChainConfig()
		require.NoError(t, err)
		if action == "remove" {
			require.Empty(t, config.Trigger)
		} else {
			require.Equal(t, "Keep title", config.ChildTitle)
			require.Equal(t, "Keep instructions", config.ChildPromptPrefix)
			require.NotNil(t, config.ChildChainConfig)
			require.Equal(t, action == "enable", config.Enabled)
		}
	}
}

func TestNewTaskStoresDraftChainWithoutRunning(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Draft chain")
	form := url.Values{"message": {"Do later"}, "category": {"backlog"}, "chain_enabled": {"true"}, "chain_trigger": {"on_completion"}, "chain_child_category": {"backlog"}, "chain_child_model": {"inherit"}}
	req := httptest.NewRequest(http.MethodPost, "/tasks?project_id="+project.ID+"&from=new&thread=1", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	tasks, err := h.taskRepo.ListByProject(context.Background(), project.ID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	config, err := tasks[0].ParseChainConfig()
	require.NoError(t, err)
	require.True(t, config.Enabled)
	require.Equal(t, "backlog", config.ChildCategory)
	executions, err := h.execRepo.ListByTask(context.Background(), tasks[0].ID)
	require.NoError(t, err)
	require.Empty(t, executions)
}
