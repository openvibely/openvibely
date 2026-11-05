package handler

import (
	"context"
	"net/http"
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
	require.Equal(t, models.TaskTag("docs/security"), task.Tag)
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
	require.Equal(t, models.TaskTag("maintenance"), saved.Tag)
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
	require.Equal(t, models.TaskTag("docs/security"), tasks[0].Tag)
	schedules, err := h.scheduleRepo.ListByTask(context.Background(), tasks[0].ID)
	require.NoError(t, err)
	require.Len(t, schedules, 1)
}
