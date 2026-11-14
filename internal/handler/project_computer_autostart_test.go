package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestComputerPanelAvailableForUnexecutedTask(t *testing.T) {
	db := teststores.New(t)
	project := &models.Project{Name: "New project"}
	require.NoError(t, db.Projects.Create(t.Context(), project))
	task := &models.Task{ProjectID: project.ID, Title: "Saved without a message", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 1}
	require.NoError(t, db.Tasks.Create(t.Context(), task))
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ready":true}`)) }))
	defer bridge.Close()
	svc := service.NewProjectComputerService(db.Projects, liveComputerFixture{service.ComputerConnection{Endpoint: bridge.URL}})
	h := &Handler{projectRepo: db.Projects, computerSvc: svc}
	e := echo.New()
	e.GET("/projects/:id/computer/status", h.ProjectComputerStatus)
	e.POST("/projects/:id/computer/:action", h.ProjectComputerLifecycle)
	request := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(method, "/projects/"+project.ID+"/computer/"+path+"?task_id="+task.ID, nil))
		return rec
	}

	missing := httptest.NewRecorder()
	e.ServeHTTP(missing, httptest.NewRequest("GET", "/projects/"+project.ID+"/computer/status", nil))
	require.Equal(t, 200, missing.Code)
	require.Contains(t, missing.Body.String(), `"enabled":false`)
	require.Contains(t, missing.Body.String(), `"configured":false`)
	rec := request("GET", "status")
	require.Equal(t, 200, rec.Code)
	var state models.ProjectComputer
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &state))
	require.False(t, state.Enabled)
	require.False(t, state.ProjectEnabled)
	require.Equal(t, 400, request("POST", "task-enable").Code)
	require.Equal(t, 200, request("POST", "enable").Code)
	require.Equal(t, 400, request("POST", "ensure").Code)
	require.Equal(t, 200, request("POST", "task-enable").Code)
	require.Equal(t, 200, request("POST", "ensure").Code)
	require.Equal(t, 200, request("POST", "disable").Code)
	rec = request("GET", "status")
	require.Equal(t, 200, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &state))
	require.False(t, state.Enabled)
	require.Equal(t, 400, request("POST", "ensure").Code)
	require.Nil(t, svc.RuntimeTools(context.Background(), project.ID, task.ID))
	require.Equal(t, 200, request("POST", "enable").Code)
	require.Equal(t, 200, request("POST", "ensure").Code)
}
