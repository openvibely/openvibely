package handler

import (
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
)

func TestCardBulkActionsEnforceEveryOwnerAndAdmin(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	ctx := t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	h.authMode = auth.AuthModeLocal
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "bulk-user", "long-password-value", "")
	require.NoError(t, err)
	userCtx := models.WithPrincipal(ctx, user.Principal)
	p := createProject(t, h, "Project 1")
	private := createProject(t, h, "Project 2")
	policy, err := tc.stores.ProjectAccess.Get(ctx, private.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, p.ID, user.UserID, "maintainer"))
	a := &models.Alert{ProjectID: p.ID, Title: "Allowed", Type: models.AlertCustom, Severity: models.SeverityInfo}
	b := &models.Alert{ProjectID: private.ID, Title: "Private", Type: models.AlertCustom, Severity: models.SeverityInfo}
	require.NoError(t, tc.stores.Alerts.Create(ctx, a))
	require.NoError(t, tc.stores.Alerts.Create(ctx, b))
	_, denied := runAction[struct{}](t, h, userCtx, "cards.bulk", cardBulkArgs{Kind: "alerts", Operation: "delete", ProjectID: models.AllProjectsID, IDs: []string{a.ID, b.ID}})
	require.True(t, denied.Code != "" || denied.Error != "")
	_, err = tc.stores.Alerts.GetByIDForProject(ctx, p.ID, a.ID)
	require.NoError(t, err)
	_, allowed := runAction[struct{}](t, h, userCtx, "cards.bulk", cardBulkArgs{Kind: "alerts", Operation: "read", ProjectID: models.AllProjectsID, IDs: []string{a.ID}})
	require.Empty(t, allowed.Error)
	require.Empty(t, allowed.Code)
	for _, kind := range []string{"models", "agents", "personalities"} {
		_, res := runAction[struct{}](t, h, userCtx, "cards.bulk", cardBulkArgs{Kind: kind, Operation: "delete", IDs: []string{"selected"}})
		require.True(t, res.Code != "" || res.Error != "", kind)
	}
	root := t.TempDir()
	h.SetAgentSkillRoot(root)
	writeStandaloneSkill(t, root, "backup", "Backup", "description", "global")
	_, res := runAction[struct{}](t, h, userCtx, "skills.operation", skillOperationArgs{Operation: "delete", Scope: "global", Handle: "backup", ProjectID: p.ID})
	require.True(t, res.Code != "" || res.Error != "")
	require.FileExists(t, filepath.Join(root, "skills", "backup", "SKILL.md"))
}
