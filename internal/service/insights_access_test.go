package service

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

type insightsAccessStore struct {
	persistence.InsightsStore
	reads, deletes int
}

func (s *insightsAccessStore) ListReports(context.Context, string, int) ([]models.InsightReport, error) {
	s.reads++
	return nil, nil
}
func (s *insightsAccessStore) DeleteInsight(context.Context, string, string) error {
	s.deletes++
	return nil
}

func TestInsightsServiceCurrentPermissions(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	org := NewOrganizationService(env.Organizations, "secret")
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "insight-reader", "long-password-value", "")
	require.NoError(t, err)
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID, OrgID: models.DefaultOrgID})
	project := &models.Project{Name: "Insights"}
	require.NoError(t, env.Projects.Create(owner, project))
	policy, err := env.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, env.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	store := &insightsAccessStore{}
	svc := NewInsightsService(store, nil, nil, nil, nil)
	svc.SetProjectAuthorization(NewProjectAccessService(env.ProjectAccess), false)
	viewer := models.WithPrincipal(ctx, user.Principal)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, "viewer"))
	_, err = svc.ListReports(viewer, project.ID, 10)
	require.NoError(t, err)
	require.Equal(t, 1, store.reads)
	for _, call := range []func() error{
		func() error { _, e := svc.RunAnalysis(viewer, project.ID); return e },
		func() error { _, e := svc.ExtractKnowledge(viewer, project.ID); return e },
		func() error { _, e := svc.RunHealthCheck(viewer, project.ID); return e },
		func() error { _, e := svc.GradeIdeas(viewer, project.ID); return e },
		func() error { return svc.DeleteInsight(viewer, project.ID, "id") },
		func() error { return svc.DeleteKnowledge(viewer, project.ID, "id") },
		func() error { return svc.AcceptInsight(viewer, project.ID, "id", nil) },
		func() error { return svc.UpdateInsightStatus(viewer, project.ID, "id", models.InsightStatusAccepted) },
	} {
		require.ErrorIs(t, call(), authz.ErrDenied)
	}
	require.Zero(t, store.deletes)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, "editor"))
	require.NoError(t, svc.DeleteInsight(viewer, project.ID, "id"))
	require.Equal(t, 1, store.deletes)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, ""))
	require.ErrorIs(t, svc.DeleteInsight(viewer, project.ID, "id"), authz.ErrDenied)
	_, err = svc.ListReports(viewer, project.ID, 10)
	require.ErrorIs(t, err, authz.ErrDenied)
	_, err = svc.ListReports(owner, "foreign", 10)
	require.ErrorIs(t, err, authz.ErrDenied)
	_, err = svc.ListReports(ctx, project.ID, 10)
	require.ErrorIs(t, err, authz.ErrDenied)
	require.Equal(t, 1, store.reads)
	require.Equal(t, 1, store.deletes)
	svc.SetProjectAuthorization(NewProjectAccessService(env.ProjectAccess), true)
	_, err = svc.ListReports(ctx, project.ID, 10)
	require.NoError(t, err)
}
