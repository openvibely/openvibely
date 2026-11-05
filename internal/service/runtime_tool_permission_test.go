package service

import (
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestRuntimeToolRoleRechecksCurrentProjectRole(t *testing.T) {
	f := newAutomationRuntimeStoreFixture(t, AutomationAdapterNativeSDLC)
	ctx := t.Context()
	org := NewOrganizationService(f.stores.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "runtime-role-user", "long-password-value", "")
	require.NoError(t, err)
	policy, err := f.stores.ProjectAccess.Get(ctx, f.project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, f.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	userCtx := models.WithPrincipal(ctx, user.Principal)
	check := func(name, input string) error {
		return requireRuntimeToolRole(userCtx, f.stores.ProjectAccess, f.stores.Organizations, f.project.ID, name, json.RawMessage(input))
	}
	cases := []struct {
		name, input string
		minimum     authz.Permission
	}{
		{"list_tasks", `{}`, authz.View},
		{"create_task", `{"title":"t"}`, authz.Edit},
		{"edit_task", `{"id":"x","auto_merge":true}`, authz.Manage},
		{"merge_task", `{}`, authz.Manage},
		{"pause_automation", `{}`, authz.Manage},
		{"update_project_settings", `{}`, authz.Manage},
		{"github_close_issue", `{}`, authz.Manage},
		{"github_get_issue", `{}`, authz.View},
		{"send_message", `{}`, authz.Edit},
	}
	for _, role := range []string{"", "viewer", "editor", "maintainer"} {
		require.NoError(t, f.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, f.project.ID, user.UserID, role))
		for _, tc := range cases {
			err := check(tc.name, tc.input)
			if authz.Rank(role) >= authz.Minimum(tc.minimum) {
				require.NoError(t, err, role+" "+tc.name)
			} else {
				require.ErrorIs(t, err, authz.ErrDenied, role+" "+tc.name)
			}
		}
		// Org-admin configuration tools never follow project roles.
		require.ErrorIs(t, check("view_settings", `{}`), authz.ErrDenied, role)
	}
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	require.NoError(t, requireRuntimeToolRole(owner, f.stores.ProjectAccess, f.stores.Organizations, f.project.ID, "view_settings", nil))
	require.ErrorIs(t, requireRuntimeToolRole(ctx, f.stores.ProjectAccess, f.stores.Organizations, f.project.ID, "list_tasks", nil), authz.ErrDenied)
	require.NoError(t, requireRuntimeToolRole(ctx, f.stores.ProjectAccess, f.stores.Organizations, f.project.ID, "create_project", nil))
}
