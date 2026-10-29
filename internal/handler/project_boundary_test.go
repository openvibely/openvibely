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

func TestTaskRoutesRejectForeignProjectBeforeHandling(t *testing.T) {
	tc := NewStoreTestContext(t)
	ctx := context.Background()
	project := &models.Project{Name: "Foreign project"}
	require.NoError(t, tc.projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "private title", Prompt: "private prompt", Category: models.CategoryBacklog, Status: models.StatusPending}
	require.NoError(t, tc.taskRepo.Create(ctx, task))
	count := 0
	for _, route := range tc.echo.Routes() {
		if route.Method == "echo_route_not_found" {
			continue
		}
		if !strings.HasPrefix(route.Path, "/tasks/:taskId") && !strings.HasPrefix(route.Path, "/api/tasks/:id/") {
			continue
		}
		count++
		t.Run(route.Method+route.Path, func(t *testing.T) {
			path := strings.ReplaceAll(route.Path, ":taskId", task.ID)
			path = strings.ReplaceAll(path, ":id", task.ID)
			path = strings.ReplaceAll(path, ":execId", "foreign-exec")
			path = strings.ReplaceAll(path, ":executionID", "foreign-exec")
			path = strings.ReplaceAll(path, ":inputId", "foreign-input")
			rec := httptest.NewRecorder()
			tc.echo.ServeHTTP(rec, httptest.NewRequest(route.Method, path+"?project_id=default", nil))
			require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
			require.NotContains(t, rec.Body.String(), task.Title)
		})
	}
	require.Greater(t, count, 40, "walk the registered task routes, including future routes")
	stored, err := tc.taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusPending, stored.Status)
	require.Equal(t, task.Title, stored.Title)
	// Positive control: matching project requests must still reach their handler.
	rec := httptest.NewRecorder()
	tc.echo.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/swarm?project_id="+project.ID, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), task.Title)
	// Selected-project context must enforce the same boundary without a query.
	require.NoError(t, tc.settingsRepo.Set(ctx, uiPreferenceSelectedProjectIDKey, "default"))
	rec = httptest.NewRecorder()
	tc.echo.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/swarm", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestReviewMutationsRejectForeignProject(t *testing.T) {
	h, e, reviews, _, _, taskID := setupReviewHandler(t)
	ctx := context.Background()
	task, err := h.taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	comment := &models.ReviewComment{TaskID: taskID, FilePath: "a.go", LineNumber: 1, LineType: "new", CommentText: "original"}
	require.NoError(t, reviews.Create(ctx, comment))
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		for _, id := range []string{comment.ID, "missing"} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/reviews/"+id+"?project_id=default", strings.NewReader(url.Values{"comment_text": {"changed"}}.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			e.ServeHTTP(rec, req)
			require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		}
	}
	comments, err := reviews.ListByTask(ctx, taskID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	require.Equal(t, "original", comments[0].CommentText)
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/reviews/"+comment.ID+"?project_id="+task.ProjectID, strings.NewReader("comment_text=changed"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
}

func TestUnknownExplicitProjectDoesNotFallBack(t *testing.T) {
	tc := NewStoreTestContext(t)
	req := httptest.NewRequest(http.MethodGet, "/?project_id=missing", nil)
	projectID, err := tc.handler.getCurrentProjectID(tc.echo.NewContext(req, httptest.NewRecorder()))
	require.Error(t, err)
	require.Empty(t, projectID)
}

func TestWebhookMutationsRejectForeignProject(t *testing.T) {
	tc := newWebhookTestContext(t)
	project := tc.CreateProject().WithName("Webhook owner").Build()
	endpoint := &models.WebhookEndpoint{ProjectID: project.ID, Name: "original", Enabled: true}
	require.NoError(t, tc.webhookRepo.Create(t.Context(), endpoint))
	for _, route := range []struct{ method, suffix string }{
		{http.MethodPut, ""}, {http.MethodPost, "/rotate-secret"}, {http.MethodPost, "/test"},
	} {
		for _, id := range []string{endpoint.ID, "missing"} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(route.method, "/channels/webhooks/"+id+route.suffix+"?project_id=default", strings.NewReader("name=changed"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			tc.echo.ServeHTTP(rec, req)
			require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		}
	}
	stored, err := tc.webhookRepo.GetByID(t.Context(), endpoint.ID)
	require.NoError(t, err)
	require.Equal(t, endpoint.Name, stored.Name)
	require.Equal(t, endpoint.Secret, stored.Secret)
	tasks, err := tc.taskRepo.ListByProject(t.Context(), project.ID, "")
	require.NoError(t, err)
	require.Empty(t, tasks)
}

func TestAPIChatStatusRejectsForeignExecutionAndQueuedInput(t *testing.T) {
	tc := NewStoreTestContext(t)
	project := tc.CreateProject().WithName("Chat owner").Build()
	task := tc.CreateTask(project.ID).Build()
	execution := &models.Execution{TaskID: task.ID, Status: models.ExecCompleted, Output: "private output"}
	require.NoError(t, tc.execRepo.Create(t.Context(), execution))
	input := &models.ThreadInput{ProjectID: project.ID, TaskID: task.ID, Scope: models.ThreadInputScopeTask, InputMode: models.ThreadInputModeQueued, InputStatus: models.ThreadInputPending, Content: "private queued input"}
	require.NoError(t, tc.stores.ThreadInputs.CreateQueued(t.Context(), input))
	tc.handler.SetThreadInputRepo(tc.stores.ThreadInputs)
	for _, id := range []string{execution.ID, input.ID} {
		rec := httptest.NewRecorder()
		tc.echo.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chat/message/"+id+"?project_id=default", nil))
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		rec = httptest.NewRecorder()
		tc.echo.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chat/message/"+id+"?project_id="+project.ID, nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
}

func TestThreadInputMutationsRejectForeignProject(t *testing.T) {
	tc := NewStoreTestContext(t)
	tc.handler.SetThreadInputRepo(tc.stores.ThreadInputs)
	project := tc.CreateProject().WithName("Input owner").Build()
	task := tc.CreateTask(project.ID).Build()
	input := &models.ThreadInput{ProjectID: project.ID, TaskID: task.ID, Scope: models.ThreadInputScopeTask, Content: "original", InputMode: models.ThreadInputModeQueued}
	require.NoError(t, tc.stores.ThreadInputs.CreateQueued(t.Context(), input))
	for _, path := range []string{
		"/thread-inputs/" + input.ID + "/edit",
		"/thread-inputs/" + input.ID + "/edit-hold",
		"/thread-inputs/" + input.ID + "/cancel",
		"/chat/queued/" + input.ID + "/steer",
		"/tasks/" + task.ID + "/thread/queued/" + input.ID + "/steer",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path+"?project_id=default", strings.NewReader("content=changed&hold=true"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		tc.echo.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	}
	stored, err := tc.stores.ThreadInputs.GetByID(t.Context(), input.ID)
	require.NoError(t, err)
	require.Equal(t, "original", stored.Content)
	require.Equal(t, models.ThreadInputPending, stored.InputStatus)
	// A task input cannot be steered through the project-chat route even in its own project.
	rec := httptest.NewRecorder()
	tc.echo.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/chat/queued/"+input.ID+"/steer?project_id="+project.ID, nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}
