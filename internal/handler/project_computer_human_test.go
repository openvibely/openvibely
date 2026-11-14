package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/testutil"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestComputerHumanImportsAndDemonstration(t *testing.T) {
	db := teststores.New(t)
	p := &models.Project{Name: "Human computer"}
	require.NoError(t, db.Projects.Create(t.Context(), p))
	task := &models.Task{ProjectID: p.ID, Title: "Source", Prompt: "Source task", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, db.Tasks.Create(t.Context(), task))
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("path") == "Files/report.txt" {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write([]byte("report content"))
			return
		}
		if r.URL.Path == "/health" {
			w.Write([]byte(`{}`))
			return
		}
		http.Error(w, "not found", 404)
	}))
	defer bridge.Close()
	svc := service.NewProjectComputerService(db.Projects, liveComputerFixture{service.ComputerConnection{Endpoint: bridge.URL}})
	svc.SetAttachmentDirectory(t.TempDir())
	scoped, err := svc.ForTask(t.Context(), p.ID, task.ID)
	require.NoError(t, err)
	require.NoError(t, scoped.SetTaskEnabled(t.Context(), p.ID, true))
	h := &Handler{projectRepo: db.Projects, computerSvc: svc, taskSvc: service.NewTaskService(db.Tasks, db.Attachments, nil)}
	llm := service.NewLLMService(db.LLMConfigs, db.Executions, db.Tasks, db.Projects, db.Schedules, db.Attachments)
	h.llmSvc = llm
	config := &models.LLMConfig{Name: "Workflow test", Provider: models.ProviderTest, Model: "test", AuthMethod: models.AuthMethodAPIKey}
	require.NoError(t, db.LLMConfigs.Create(t.Context(), config))
	p.DefaultAgentConfigID = &config.ID
	require.NoError(t, db.Projects.Update(t.Context(), p))
	mock := testutil.NewMockLLMCaller()
	mock.Response = `{"name":"Report workflow","instructions":"Open Reports. Download {{report_period}}. Verify the file exists."}`
	mock.TextOnly = mock.Response
	var temporaryFile string
	mock.OnCall = func(_ context.Context, call testutil.MockLLMCall) {
		require.Len(t, call.Attachments, 1)
		temporaryFile = call.Attachments[0].FilePath
		_, err := os.Stat(temporaryFile)
		require.NoError(t, err)
	}
	llm.SetLLMCaller(mock)
	e := echo.New()
	e.POST("/projects/:id/computer/import-file", h.ProjectComputerImportFile)
	e.POST("/projects/:id/computer/teach", h.ProjectComputerTeach)
	e.POST("/projects/:id/computer/workflows", h.ProjectComputerWorkflows)
	send := func(action string, body any, origin string) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/projects/"+p.ID+"/computer/"+action+"?task_id="+task.ID+"&path="+url.QueryEscape("report.txt"), bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	require.Equal(t, 400, send("import-file", nil, "").Code)
	store, err := db.Projects.Computers().ForTask(t.Context(), p.ID, task.ID)
	require.NoError(t, err)
	require.NoError(t, store.Hold(t.Context(), p.ID, true))
	require.Equal(t, 403, send("import-file", nil, "https://other.test").Code)
	rec := send("import-file", nil, "")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	attachments, err := db.Attachments.ListByTask(t.Context(), task.ID)
	require.NoError(t, err)
	require.Len(t, attachments, 1)
	data, err := os.ReadFile(attachments[0].FilePath)
	require.NoError(t, err)
	require.Equal(t, "report content", string(data))
	var capture bytes.Buffer
	require.NoError(t, jpeg.Encode(&capture, image.NewRGBA(image.Rect(0, 0, 10, 10)), nil))
	demo := service.ComputerDemonstration{Goal: "Download the report", Steps: []string{"Open Reports", "Type [user-provided value] into the focused field."}, Images: []string{base64.StdEncoding.EncodeToString(capture.Bytes())}}
	require.Equal(t, 403, send("teach", demo, "https://other.test").Code)
	rec = send("teach", demo, "")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.True(t, mock.LastAgentRequest().DisableTools)
	require.Nil(t, llmcontracts.RuntimeToolsFromContext(mock.LastAgentRequest().Ctx))
	_, err = os.Stat(temporaryFile)
	require.True(t, os.IsNotExist(err))
	var draft models.ComputerWorkflow
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &draft))
	require.Equal(t, "Report workflow", draft.Name)
	tasks, err := db.Tasks.ListByProject(t.Context(), p.ID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	workflows, err := svc.Workflows(t.Context(), p.ID)
	require.NoError(t, err)
	require.Empty(t, workflows)
	draft.Instructions = "Reviewed reusable instructions with {{period}}"
	rec = send("workflows", map[string]string{"action": "save", "name": draft.Name, "instructions": draft.Instructions}, "")
	require.Equal(t, 201, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &draft))
	draft.Instructions = "Edited saved workflow"
	rec = send("workflows", map[string]string{"action": "save", "id": draft.ID, "name": draft.Name, "instructions": draft.Instructions}, "")
	require.Equal(t, 201, rec.Code, rec.Body.String())
	workflows, err = svc.Workflows(t.Context(), p.ID)
	require.NoError(t, err)
	require.Len(t, workflows, 1)
	require.Equal(t, draft.ID, workflows[0].ID)
	for i := 0; i < 2; i++ {
		rec = send("workflows", map[string]string{"action": "use", "id": draft.ID}, "")
		require.Equal(t, 201, rec.Code, rec.Body.String())
		var result map[string]string
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
		saved, err := db.Tasks.GetByID(t.Context(), result["task_id"])
		require.NoError(t, err)
		require.Equal(t, models.CategoryBacklog, saved.Category)
		require.Equal(t, draft.Instructions, saved.Prompt)
	}
	other := &models.Project{Name: "Other"}
	require.NoError(t, db.Projects.Create(t.Context(), other))
	_, err = svc.UseWorkflow(t.Context(), other.ID, draft.ID, h.taskSvc)
	require.Error(t, err)
	require.Equal(t, 403, send("workflows", map[string]string{"action": "delete", "id": draft.ID}, "https://other.test").Code)
	require.Equal(t, 200, send("workflows", map[string]string{"action": "delete", "id": draft.ID}, "").Code)
	workflows, err = svc.Workflows(t.Context(), p.ID)
	require.NoError(t, err)
	require.Empty(t, workflows)
	mock.Response = "invalid json"
	mock.TextOnly = mock.Response
	require.Equal(t, 400, send("teach", demo, "").Code)
	demo.Images = []string{"invalid"}
	require.Equal(t, 400, send("teach", demo, "").Code)
	demo.Images = nil
	demo.Goal = "Private attempt"
	require.NoError(t, store.RequestHandoff(t.Context(), p.ID, "request_browser_login", "https://example.test", "Sign in", "Dashboard"))
	require.Equal(t, 400, send("teach", demo, "").Code)
	require.NoError(t, scoped.CancelHandoff(t.Context(), p.ID))
	require.Equal(t, 400, send("teach", demo, "").Code)
}
