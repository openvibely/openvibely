package handler

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestGlobalRuntimeToolsRequireCurrentOrgAdmin(t *testing.T) {
	tc := NewStoreTestContext(t)
	h, ctx := tc.handler, t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "global-tools-user", "long-password-value", "")
	require.NoError(t, err)
	project := createProject(t, h, "Global tools")
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, "maintainer"))
	names := []string{"view_settings", "list_channels", "view_system_update", "list_models", "get_model", "list_personalities", "get_personality", "set_personality", "save_custom_personality", "list_agents", "create_agent", "update_agent"}
	calls := 0
	handlers := map[string]chatcontrol.RuntimeActionHandler{}
	for _, name := range names {
		handlers[name] = func(context.Context, json.RawMessage) (string, error) { calls++; return "ok", nil }
	}
	h.authorizeProjectRuntimeHandlers(project.ID, handlers)
	principal := user.Principal
	principal.Role = "owner" // forged/stale claim is not authority
	userCtx := models.WithPrincipal(ctx, principal)
	for _, name := range names {
		_, err := handlers[name](userCtx, json.RawMessage(`{}`))
		require.ErrorIs(t, err, authz.ErrDenied, name)
	}
	require.Zero(t, calls)
	require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "admin", false))
	for _, name := range names {
		_, err := handlers[name](userCtx, json.RawMessage(`{}`))
		require.NoError(t, err, name)
	}
	require.Equal(t, len(names), calls)
	require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "member", false))
	for _, name := range names {
		_, err := handlers[name](userCtx, json.RawMessage(`{}`))
		require.ErrorIs(t, err, authz.ErrDenied, name)
	}
	require.Equal(t, len(names), calls, "existing handlers must recheck demotion")
	h.authMode = auth.AuthModeLocal
	_, err = handlers["view_settings"](ctx, json.RawMessage(`{}`))
	require.ErrorIs(t, err, authz.ErrDenied)
	h.authMode = auth.AuthModeDisabled
	_, err = handlers["view_settings"](ctx, json.RawMessage(`{}`))
	require.NoError(t, err)
}
