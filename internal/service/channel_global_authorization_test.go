package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestChannelGlobalActionsCurrentMembership(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID, OrgID: models.DefaultOrgID})
	project := &models.Project{Name: "Global tools"}
	require.NoError(t, env.Projects.Create(owner, project))
	org := NewOrganizationService(env.Organizations, "secret")
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "channel-member", "long-password-value", "")
	require.NoError(t, err)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, "maintainer"))
	worker := &WorkerService{runOrganizations: env.Organizations, runAccess: env.ProjectAccess}
	names := []string{"view_settings", "list_channels", "list_models", "get_model", "list_personalities", "get_personality", "set_personality", "save_custom_personality", "list_agents", "create_agent", "update_agent"}
	calls := 0
	handlers := map[string]chatcontrol.RuntimeActionHandler{}
	for _, name := range names {
		handlers[name] = func(context.Context, json.RawMessage) (string, error) { calls++; return "ok", nil }
	}
	guardChannelGlobalActions(handlers, worker, project.ID)
	principal := user.Principal
	principal.Role = "owner"
	member := models.WithPrincipal(ctx, principal)
	for _, name := range names {
		_, err := handlers[name](member, nil)
		require.ErrorIs(t, err, authz.ErrDenied, name)
	}
	require.Zero(t, calls)
	require.NoError(t, env.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "admin", false))
	for _, name := range names {
		_, err := handlers[name](member, nil)
		require.NoError(t, err, name)
	}
	require.Equal(t, len(names), calls)
	require.NoError(t, env.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "member", false))
	for _, name := range names {
		_, err := handlers[name](member, nil)
		require.ErrorIs(t, err, authz.ErrDenied, name)
	}
	require.Equal(t, len(names), calls)
	_, err = handlers["view_settings"](ctx, nil)
	require.ErrorIs(t, err, authz.ErrDenied)
	worker.runAuthOff = true
	_, err = handlers["view_settings"](ctx, nil)
	require.NoError(t, err)
	// The real builder must install the guard before nil data dependencies run.
	real := buildChannelUtilityActionHandlers(channelUtilityActionHandlerOptions{WorkerSvc: worker, ProjectID: project.ID})
	for _, name := range names {
		_, err := real[name](member, nil)
		require.ErrorIs(t, err, authz.ErrDenied, name)
	}
	foreign := map[string]chatcontrol.RuntimeActionHandler{"view_settings": func(context.Context, json.RawMessage) (string, error) {
		t.Fatal("foreign project reached handler")
		return "", nil
	}}
	guardChannelGlobalActions(foreign, worker, "missing-project")
	_, err = foreign["view_settings"](owner, nil)
	require.ErrorIs(t, err, authz.ErrDenied)
}
