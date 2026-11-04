package handler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestProjectRuntimePermissionMatrix(t *testing.T) {
	tc := NewStoreTestContext(t)
	ctx := t.Context()
	h := tc.handler
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	bob, err := org.Accept(ctx, invite, "runtime-bob", "bob-password-long", "")
	require.NoError(t, err)
	project := createProject(t, h, "Runtime project")
	policy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	cases := []struct {
		name, input string
		permission  authz.Permission
	}{
		{"list_tasks", `{}`, authz.View},
		{"list_alerts", `{}`, authz.View},
		{"get_alert", `{}`, authz.View},
		{"view_pulse", `{}`, authz.View},
		{"memory_view", `{}`, authz.View},
		{"view_usage_analytics", `{}`, authz.View},
		{"create_alert", `{}`, authz.Edit},
		{"create_notification", `{}`, authz.Edit},
		{"decide_alert", `{}`, authz.Edit},
		{"claim_alert", `{}`, authz.Edit},
		{"delete_alert", `{}`, authz.Edit},
		{"toggle_alert", `{}`, authz.Edit},
		{"list_tasks", "", authz.View},
		{"list_schedules", `{}`, authz.View},
		{"create_task", `{"title":"test"}`, authz.Edit},
		{"schedule_task", `{}`, authz.Edit},
		{"send_to_task", `{}`, authz.Edit},
		{"set_task_goal", `{}`, authz.Edit},
		{"merge_task", `{}`, authz.Manage},
		{"create_task", `{"auto_merge":true}`, authz.Manage},
		{"edit_task", `{"auto_merge":false}`, authz.Manage},
		{"create_swarm_task", `{"auto_merge_on_goal_achieved":true}`, authz.Manage},
	}
	for _, role := range []string{"", "viewer", "editor", "maintainer", "owner"} {
		principal := bob.Principal
		if role == "owner" {
			principal = models.Principal{UserID: models.BootstrapUserID}
		} else {
			require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, bob.UserID, role))
		}
		for _, test := range cases {
			t.Run(role+"/"+test.name+test.input, func(t *testing.T) {
				called := false
				handlers := map[string]chatcontrol.RuntimeActionHandler{test.name: func(context.Context, json.RawMessage) (string, error) { called = true; return "ok", nil }}
				h.authorizeProjectRuntimeHandlers(project.ID, handlers)
				_, err := handlers[test.name](models.WithPrincipal(ctx, principal), json.RawMessage(test.input))
				allowed := authz.Rank(role) >= authz.Minimum(test.permission)
				if allowed {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, authz.ErrDenied)
				}
				require.Equal(t, allowed, called, "denied calls must not reach the action")
			})
		}
	}
}

func TestProjectRuntimeRejectsForeignObjectsAndRevokedAccess(t *testing.T) {
	tc := NewStoreTestContext(t)
	ctx := t.Context()
	h := tc.handler
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	project := createProject(t, h, "Runtime source")
	foreign := createProject(t, h, "Other project")
	task := createTask(t, h, foreign.ID, "Foreign task", func(task *models.Task) { task.Category = models.CategoryBacklog })
	schedule := createSchedule(t, h, task.ID, time.Now().Add(time.Hour))
	foreignAlert := models.Alert{ProjectID: foreign.ID, Title: "Foreign alert", Type: "custom", Severity: "info"}
	require.NoError(t, h.alertSvc.Create(ctx, &foreignAlert))
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	for _, test := range []struct{ name, input string }{
		{"edit_task", `{"id":"` + task.ID + `"}`},
		{"get_alert", `{"alert_id":"` + foreignAlert.ID + `"}`},
		{"delete_alert", `{"id":"` + foreignAlert.ID + `"}`},
		{"view_task_thread", `{"task_id":"` + task.ID + `"}`},
		{"modify_schedule", `{"schedule_id":"` + schedule.ID + `"}`},
		{"list_tasks", `{"project_id":"` + foreign.ID + `"}`},
	} {
		require.ErrorIs(t, h.requireProjectRuntimeAction(owner, project.ID, test.name, authz.View, json.RawMessage(test.input)), authz.ErrDenied)
	}
	// A real executor must recheck after it has already been built and used.
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	bob, err := org.Accept(ctx, invite, "revoked-bob", "bob-password-long", "")
	require.NoError(t, err)
	policy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, bob.UserID, "viewer"))
	executor := h.chatActionExecutor(streamingResponseParams{ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceAPI)
	userCtx := models.WithPrincipal(ctx, bob.Principal)
	_, handled, isError, err := executor(userCtx, "list_tasks", json.RawMessage(`{}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isError)
	ownTask := createTask(t, h, project.ID, "Unchanged", func(task *models.Task) { task.Category = models.CategoryBacklog })
	_, handled, isError, err = executor(userCtx, "edit_task", json.RawMessage(`{"id":"`+ownTask.ID+`","title":"Unauthorized edit"}`))
	require.True(t, handled)
	require.True(t, isError)
	require.ErrorIs(t, err, authz.ErrDenied)
	stored, err := h.taskRepo.GetByID(ctx, ownTask.ID)
	require.NoError(t, err)
	require.Equal(t, "Unchanged", stored.Title)
	alert := models.Alert{ProjectID: project.ID, Title: "Keep me", Type: "custom", Severity: "info"}
	require.NoError(t, h.alertSvc.Create(ctx, &alert))
	_, handled, isError, err = executor(userCtx, "delete_alert", json.RawMessage(`{"id":"`+alert.ID+`"}`))
	require.True(t, handled)
	require.True(t, isError)
	require.ErrorIs(t, err, authz.ErrDenied)
	retained, err := h.alertSvc.GetByID(ctx, project.ID, alert.ID)
	require.NoError(t, err)
	require.NotNil(t, retained)
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, bob.UserID, ""))
	_, handled, isError, err = executor(userCtx, "list_tasks", json.RawMessage(`{}`))
	require.True(t, handled)
	require.True(t, isError)
	require.ErrorIs(t, err, authz.ErrDenied)
	for _, name := range []string{"list_alerts", "view_pulse", "view_usage_analytics"} {
		_, handled, isError, err = executor(userCtx, name, json.RawMessage(`{}`))
		require.True(t, handled)
		require.True(t, isError)
		require.ErrorIs(t, err, authz.ErrDenied)
	}
	// Missing identity never falls back to the bootstrap owner. Auth-off callers
	// carry the owner principal supplied by the existing authentication middleware.
	require.ErrorIs(t, h.requireProjectRuntimeAction(ctx, project.ID, "list_tasks", authz.View, json.RawMessage(`{}`)), authz.ErrDenied)
	require.NoError(t, h.requireProjectRuntimeAction(owner, project.ID, "list_tasks", authz.View, json.RawMessage(`{}`)))
}

func TestProjectRuntimeAuthOffAndMissingAuthenticatedIdentity(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	for _, mode := range []auth.AuthMode{auth.AuthModeDisabled, auth.AuthModeLocal, auth.AuthModeHostedSSO} {
		t.Run(string(mode), func(t *testing.T) {
			h.SetAuthMode(mode)
			executor := h.chatActionExecutor(streamingResponseParams{ProjectID: "default"}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceAPI)
			_, handled, isError, err := executor(t.Context(), "list_tasks", json.RawMessage(`{}`))
			require.True(t, handled)
			if mode == auth.AuthModeDisabled {
				require.NoError(t, err)
				require.False(t, isError)
			} else {
				require.ErrorIs(t, err, authz.ErrDenied)
				require.True(t, isError)
			}
		})
	}
}

func TestProjectRuntimeReportingAuthModes(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	for _, mode := range []auth.AuthMode{auth.AuthModeDisabled, auth.AuthModeLocal, auth.AuthModeHostedSSO} {
		h.SetAuthMode(mode)
		for _, name := range []string{"view_pulse", "view_usage_analytics", "list_alerts", "create_alert"} {
			called := false
			executor := h.authorizeProjectRuntimeExecutor("default", func(ctx context.Context, _ string, _ json.RawMessage) (string, bool, bool, error) {
				called = true
				principal, ok := models.PrincipalFromContext(ctx)
				require.True(t, ok)
				require.Equal(t, models.BootstrapUserID, principal.UserID)
				return "ok", true, false, nil
			})
			_, _, _, err := executor(t.Context(), name, json.RawMessage(`{}`))
			if mode == auth.AuthModeDisabled {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, authz.ErrDenied)
			}
			require.Equal(t, mode == auth.AuthModeDisabled, called)
		}
	}
}
