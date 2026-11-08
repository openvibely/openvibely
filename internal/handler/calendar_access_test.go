package handler

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCalendarPermissionsAndAudit(t *testing.T) {
	tc := NewStoreTestContext(t)
	h, ctx := tc.handler, t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	project := createProject(t, h, "Calendar access")
	policy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "calendar-user", "long-password-value", "")
	require.NoError(t, err)
	e := echo.New()
	e.Use(h.projectAccessBoundary)
	e.GET("/schedule/calendar-state", h.ScheduleCalendarState)
	e.POST("/schedule/calendar-action", h.ScheduleCalendarAction)
	request := func(method, path, body string, p models.Principal) int {
		req := httptest.NewRequest(method, path+"?project_id="+project.ID, strings.NewReader(body)).WithContext(models.WithPrincipal(ctx, p))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}
	for _, role := range []string{"viewer", "editor", "maintainer"} {
		require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, role))
		require.Equal(t, 200, request("GET", "/schedule/calendar-state", "", user.Principal))
		want := 200
		if role == "viewer" {
			want = 403
		}
		require.Equal(t, want, request("POST", "/schedule/calendar-action", `{"action":"pause_all"}`, user.Principal))
		state, err := tc.stores.Schedules.CalendarState(ctx, project.ID)
		require.NoError(t, err)
		require.Equal(t, role != "viewer", state.Paused)
	}
	events, err := tc.stores.ProjectAccess.Audit(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, "schedule.calendar.pause_all", events[0].Action)
	require.Equal(t, user.UserID, events[0].ActorUserID)
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, ""))
	require.Equal(t, 404, request("GET", "/schedule/calendar-state", "", user.Principal))
	require.Equal(t, 404, request("POST", "/schedule/calendar-action", `{"action":"resume_all"}`, user.Principal))
	// Direct persistence callers with stale claims are checked inside the transaction too.
	_, err = tc.stores.Schedules.ApplyCalendarAction(models.WithPrincipal(ctx, user.Principal), project.ID, models.ScheduleCalendarAction{Action: "resume_all"}, time.Now())
	require.Error(t, err)
	state, err := tc.stores.Schedules.CalendarState(ctx, project.ID)
	require.NoError(t, err)
	require.True(t, state.Paused)
	require.Equal(t, 200, request("POST", "/schedule/calendar-action", `{"action":"resume_all"}`, models.Principal{UserID: models.BootstrapUserID}))
}
