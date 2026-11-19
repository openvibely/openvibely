package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestCustomTagsFormCreationEditingAndValidation(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	form := url.Values{"title": {"Custom task"}, "prompt": {"Work"}, "category": {"backlog"}, "priority": {"2"}, "tag": {" Docs/Security "}}
	assertCode(t, htmxPost(e, "/tasks?project_id=default", form), http.StatusOK)
	tasks, err := h.taskSvc.ListByProject(ctx, "default", "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	task := tasks[0]
	require.Equal(t, models.TaskTag("Docs/Security"), task.Tag)
	draft := htmxGet(e, "/tasks/new?project_id=default")
	assertCode(t, draft, http.StatusOK)
	require.NotContains(t, draft.Body.String(), `data-value="Docs/Security"`)
	options := htmxGet(e, "/api/tasks/tags?project_id=default")
	assertCode(t, options, http.StatusOK)
	require.JSONEq(t, `["Docs/Security"]`, options.Body.String())
	form.Del("tag")
	form.Set("prompt", "Updated")
	assertCode(t, htmxPut(e, "/tasks/"+task.ID, form), http.StatusOK)
	saved, err := h.taskSvc.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, task.Tag, saved.Tag)
	form.Set("tag", strings.Repeat("x", 65))
	rec := htmxPut(e, "/tasks/"+task.ID, form)
	assertCode(t, rec, http.StatusBadRequest)
	require.Contains(t, rec.Body.String(), models.ErrInvalidTaskTag.Error())
	form.Set("tag", " Maintenance ")
	assertCode(t, htmxPut(e, "/tasks/"+task.ID, form), http.StatusOK)
	saved, err = h.taskSvc.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.TaskTag("Maintenance"), saved.Tag)
	form.Set("tag", "")
	assertCode(t, htmxPut(e, "/tasks/"+task.ID, form), http.StatusOK)
	saved, err = h.taskSvc.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Empty(t, saved.Tag)
	form.Set("title", "Invalid")
	form.Set("tag", "bad\ntag")
	rec = htmxPost(e, "/tasks?project_id=default", form)
	assertCode(t, rec, http.StatusBadRequest)
	require.Contains(t, rec.Body.String(), models.ErrInvalidTaskTag.Error())
}

func TestCustomTagScheduledCreation(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	form := url.Values{"title": {"Scheduled custom task"}, "prompt": {"Work"}, "category": {"scheduled"}, "priority": {"2"}, "tag": {" Docs/Security "}, "run_at": {"2035-02-22T10:00"}, "repeat_type": {"daily"}, "repeat_interval": {"1"}}
	assertCode(t, htmxPost(e, "/tasks?project_id=default", form), http.StatusOK)
	tasks, err := h.taskSvc.ListByProject(context.Background(), "default", string(models.CategoryScheduled))
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, models.TaskTag("Docs/Security"), tasks[0].Tag)
	schedules, err := h.scheduleRepo.ListByTask(context.Background(), tasks[0].ID)
	require.NoError(t, err)
	require.Len(t, schedules, 1)
}

func TestTaskTagsReusableAcrossOpenTasks(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	a := createTask(t, h, "default", "Source")
	b := createTask(t, h, "default", "Destination")
	page := htmxGet(e, "/tasks/"+b.ID+"?project_id=default&tab=details")
	assertCode(t, page, http.StatusOK)
	require.NotContains(t, page.Body.String(), `data-value="Release QA"`)
	assign := func(taskID, tag string) {
		body := url.Values{"field": {"tag"}, "value": {tag}}
		req := httptest.NewRequest(http.MethodPatch, "/tasks/"+taskID+"/details/property?project_id=default", strings.NewReader(body.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		assertCode(t, rec, http.StatusOK)
	}
	assign(a.ID, "Release QA")
	options := htmxGet(e, "/api/tasks/tags?project_id=default")
	assertCode(t, options, http.StatusOK)
	var tags []string
	require.NoError(t, json.Unmarshal(options.Body.Bytes(), &tags))
	require.Equal(t, []string{"Release QA"}, tags)
	assign(b.ID, tags[0])
	saved, err := h.taskRepo.GetByID(t.Context(), b.ID)
	require.NoError(t, err)
	require.Equal(t, models.TaskTag("Release QA"), saved.Tag)
}
