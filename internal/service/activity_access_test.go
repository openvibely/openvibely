package service

import (
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestActivityServicePermissionRechecks(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	org := NewOrganizationService(env.Organizations, "secret")
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "activity-reader", "long-password-value", "")
	require.NoError(t, err)
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID, OrgID: models.DefaultOrgID})
	project := &models.Project{Name: "Activity"}
	require.NoError(t, env.Projects.Create(owner, project))
	policy, err := env.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, env.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, "viewer"))
	viewer := models.WithPrincipal(ctx, user.Principal)
	svc := NewUpcomingService(env.Upcoming)
	svc.SetProjectAuthorization(NewProjectAccessService(env.ProjectAccess), false)
	_, err = ExecuteViewPulseTool(viewer, svc, project.ID, json.RawMessage(`{}`))
	require.NoError(t, err)
	_, err = svc.GenerateHistory(viewer, project.ID, models.TimeRange("day"))
	require.NoError(t, err)
	// No model configured: permission denial must precede any model lookup/work.
	_, err = svc.GeneratePulseSummary(viewer, project.ID, nil)
	require.ErrorIs(t, err, authz.ErrDenied)
	_, err = svc.GenerateReflectionSummary(viewer, project.ID, nil)
	require.ErrorIs(t, err, authz.ErrDenied)
	_, err = ExecuteViewPulseTool(viewer, svc, "foreign", json.RawMessage(`{}`))
	require.ErrorIs(t, err, authz.ErrDenied)
	_, err = ExecuteViewPulseTool(ctx, svc, project.ID, json.RawMessage(`{}`))
	require.ErrorIs(t, err, authz.ErrDenied)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, ""))
	_, err = ExecuteViewPulseTool(viewer, svc, project.ID, json.RawMessage(`{}`))
	require.ErrorIs(t, err, authz.ErrDenied)
	_, err = svc.GenerateHistory(viewer, project.ID, models.TimeRange("day"))
	require.ErrorIs(t, err, authz.ErrDenied)
	svc.SetProjectAuthorization(NewProjectAccessService(env.ProjectAccess), true)
	_, err = ExecuteViewPulseTool(ctx, svc, project.ID, json.RawMessage(`{}`))
	require.NoError(t, err)
}
