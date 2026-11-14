package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestComputerPanelCompletionResumesExactTask(t *testing.T) {
	h, _, configs := setupTestHandler(t)
	h.workerSvc = nil
	agent := createAgent(t, configs)
	project := createProject(t, h, "Computer continuation")
	task := createTask(t, h, project.ID, "Original assignment", func(task *models.Task) {
		task.Status = models.StatusCompleted
		task.Category = models.CategoryCompleted
		task.AgentID = &agent.ID
	})
	createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) { ex.Status = models.ExecCompleted; ex.PromptSent = "Original assignment" })
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"verified":true}`) }))
	defer bridge.Close()
	h.computerSvc = service.NewProjectComputerService(h.projectRepo, liveComputerFixture{service.ComputerConnection{Endpoint: bridge.URL, Token: "fixture"}})
	require.NoError(t, h.projectRepo.Computers().Configure(t.Context(), project.ID, "local"))
	if err := h.projectRepo.Computers().SetEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	scoped, err := h.projectRepo.Computers().ForTask(t.Context(), project.ID, task.ID)
	require.NoError(t, err)
	require.NoError(t, scoped.SetEnabled(t.Context(), project.ID, true))
	require.NoError(t, scoped.RequestHandoff(t.Context(), project.ID, "request_browser_login", "https://example.test/account", "Open account", "Dashboard"))
	started := make(chan testutil.MockLLMCall, 2)
	mock := testutil.NewMockLLMCaller()
	mock.Response = "Resumed"
	mock.TextOnly = "Resumed"
	mock.OnCall = func(_ context.Context, call testutil.MockLLMCall) { started <- call }
	h.llmSvc.SetLLMCaller(mock)
	e := echo.New()
	e.POST("/projects/:id/computer/:action", h.ProjectComputerLifecycle)
	path := "/projects/" + project.ID + "/computer/login-done?task_id=" + task.ID
	record := httptest.NewRecorder()
	e.ServeHTTP(record, httptest.NewRequest("POST", path, nil))
	require.Equal(t, 200, record.Code, record.Body.String())
	select {
	case call := <-started:
		require.Contains(t, call.Prompt, "Continue the original task")
	case <-time.After(5 * time.Second):
		t.Fatal("task did not resume")
	}
	record = httptest.NewRecorder()
	e.ServeHTTP(record, httptest.NewRequest("POST", path, nil))
	require.Equal(t, 400, record.Code)
	require.Eventually(t, func() bool {
		executions, err := h.execRepo.ListByTask(t.Context(), task.ID)
		if err != nil {
			return false
		}
		for _, ex := range executions {
			if strings.Contains(ex.PromptSent, "Continue the original task") && ex.Status == models.ExecCompleted {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)
}

func TestComputerPanelVisualCheckResumesOriginalTask(t *testing.T) {
	h, _, configs := setupTestHandler(t)
	h.workerSvc = nil
	agent := createAgent(t, configs)
	project := createProject(t, h, "Visual login")
	task := createTask(t, h, project.ID, "Original assignment", func(task *models.Task) {
		task.Status = models.StatusCompleted
		task.Category = models.CategoryCompleted
		task.AgentID = &agent.ID
	})
	createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) { ex.Status = models.ExecCompleted; ex.PromptSent = "Original assignment" })
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"verified":false,"url":"https://example.test/account","screenshot":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC1lEQVR42mP8/x8AAusB9Wl6iWQAAAAASUVORK5CYII="}`)
	}))
	defer bridge.Close()
	h.computerSvc = service.NewProjectComputerService(h.projectRepo, liveComputerFixture{service.ComputerConnection{Endpoint: bridge.URL, Token: "fixture"}})
	h.llmSvc.SetProjectComputerService(h.computerSvc)
	require.NoError(t, h.projectRepo.Computers().Configure(t.Context(), project.ID, "local"))
	if err := h.projectRepo.Computers().SetEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	scoped, err := h.projectRepo.Computers().ForTask(t.Context(), project.ID, task.ID)
	require.NoError(t, err)
	require.NoError(t, scoped.SetEnabled(t.Context(), project.ID, true))
	require.NoError(t, scoped.RequestHandoff(t.Context(), project.ID, "request_browser_login", "https://example.test/account", "Read account", "Account dashboard"))
	calls := make(chan testutil.MockLLMCall, 4)
	mock := testutil.NewMockLLMCaller()
	mock.Response = `{"verified":true}`
	mock.TextOnly = mock.Response
	mock.OnCall = func(_ context.Context, call testutil.MockLLMCall) { calls <- call }
	h.llmSvc.SetLLMCaller(mock)
	e := echo.New()
	e.POST("/projects/:id/computer/:action", h.ProjectComputerLifecycle)
	record := httptest.NewRecorder()
	e.ServeHTTP(record, httptest.NewRequest("POST", "/projects/"+project.ID+"/computer/login-done?task_id="+task.ID, nil))
	require.Equal(t, 200, record.Code, record.Body.String())
	for index := 0; index < 2; index++ {
		select {
		case call := <-calls:
			if index == 0 {
				require.Len(t, call.Attachments, 1)
			} else {
				require.Contains(t, call.Prompt, "Continue the original task")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("verification or continuation missing")
		}
	}
	require.Eventually(t, func() bool {
		executions, err := h.execRepo.ListByTask(t.Context(), task.ID)
		if err != nil {
			return false
		}
		for _, ex := range executions {
			if strings.Contains(ex.PromptSent, "Continue the original task") && ex.Status == models.ExecCompleted {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)
}
