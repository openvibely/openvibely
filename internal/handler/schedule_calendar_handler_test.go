package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestScheduleCalendarActionBulkSelectionAndProjectIsolation(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewScheduleRepo(db)
	tasks := repository.NewTaskRepo(db, nil)
	h := &Handler{scheduleRepo: repo}
	ctx := context.Background()
	var ids []string
	for i := 0; i < 2; i++ {
		task := &models.Task{ProjectID: "default", Title: fmt.Sprintf("Calendar %d", i), Prompt: "test", Category: models.CategoryScheduled, Status: models.StatusPending}
		require.NoError(t, tasks.Create(ctx, task))
		s := &models.Schedule{TaskID: task.ID, RunAt: time.Now().Add(time.Hour), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
		require.NoError(t, repo.Create(ctx, s))
		ids = append(ids, s.ID)
	}
	call := func(project string, action models.ScheduleCalendarAction) (*httptest.ResponseRecorder, error) {
		data, err := json.Marshal(action)
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/schedule/calendar-action?project_id="+project, bytes.NewReader(data))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		err = h.ScheduleCalendarAction(echo.New().NewContext(req, rec))
		return rec, err
	}
	_, err := call("other-project", models.ScheduleCalendarAction{Action: "pause", ScheduleIDs: ids})
	var httpErr *echo.HTTPError
	require.ErrorAs(t, err, &httpErr)
	require.Equal(t, http.StatusBadRequest, httpErr.Code)
	for _, id := range ids {
		s, err := repo.GetByID(ctx, id)
		require.NoError(t, err)
		require.True(t, s.Enabled)
	}
	rec, err := call("default", models.ScheduleCalendarAction{Action: "pause", ScheduleIDs: ids})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	for _, id := range ids {
		s, err := repo.GetByID(ctx, id)
		require.NoError(t, err)
		require.False(t, s.Enabled)
	}
	var result struct {
		Undo models.ScheduleCalendarAction `json:"undo"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	_, err = call("default", result.Undo)
	require.NoError(t, err)
	for _, id := range ids {
		s, err := repo.GetByID(ctx, id)
		require.NoError(t, err)
		require.True(t, s.Enabled)
	}
}

func TestScheduleCalendarRoutesPersistAndRenderState(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	action := models.ScheduleCalendarAction{Action: "skip", Skips: []models.ScheduleSkip{{StartAt: time.Now().Truncate(time.Hour).Unix(), EndAt: time.Now().Truncate(time.Hour).Add(time.Hour).Unix()}}}
	data, err := json.Marshal(action)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/schedule/calendar-action?project_id="+project.ID, bytes.NewReader(data))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	tc.echo.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	stateResponse := tc.HTTP().Get("/schedule/calendar-state?project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusOK, stateResponse.Code)
	var state models.ScheduleCalendarState
	require.NoError(t, json.Unmarshal(stateResponse.Body.Bytes(), &state))
	require.Equal(t, action.Skips, state.Skips)
	page := tc.HTMX().Get("/schedule?project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusOK, page.Code)
	require.Contains(t, page.Body.String(), fmt.Sprint(action.Skips[0].StartAt))
	require.Contains(t, page.Body.String(), "schedule-calendar-controls")
}
