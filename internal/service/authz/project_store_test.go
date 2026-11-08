package authz_test

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestProjectStoreFiltersEveryProjectionAndGuardsMutations(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	org := service.NewOrganizationService(env.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	bob, err := org.Accept(ctx, invite, "bob", "bob-password-long", "")
	require.NoError(t, err)
	bobCtx := models.WithPrincipal(ctx, bob.Principal)
	ownerCtx := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID, OrgID: models.DefaultOrgID})
	project := &models.Project{Name: "Private fixture", RepoPath: "/private-repository"}
	require.NoError(t, env.Projects.Create(ownerCtx, project))
	policy, err := env.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, env.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	store := authz.NewProjectStore(env.Projects, &authz.Authorizer{Store: env.ProjectAccess})
	checkAbsent := func() {
		t.Helper()
		projects, err := store.List(bobCtx)
		require.NoError(t, err)
		for _, p := range projects {
			require.NotEqual(t, project.ID, p.ID)
		}
		selectors, err := store.ListSelectorOptions(bobCtx)
		require.NoError(t, err)
		for _, p := range selectors {
			require.NotEqual(t, project.ID, p.ID)
		}
		api, err := store.ListAPIProjects(bobCtx)
		require.NoError(t, err)
		for _, p := range api {
			require.NotEqual(t, project.ID, p.ID)
		}
		capacity, err := store.ListWorkerCapacityProjects(bobCtx)
		require.NoError(t, err)
		for _, p := range capacity {
			require.NotEqual(t, project.ID, p.ID)
		}
		validation, err := store.ListRepoValidationProjects(bobCtx)
		require.NoError(t, err)
		for _, p := range validation {
			require.NotEqual(t, project.ID, p.ID)
		}
		require.NoError(t, store.ForEachRepoRoot(bobCtx, func(p persistence.ProjectRepoRoot) { require.NotEqual(t, project.ID, p.ID) }))
		_, err = store.GetByID(bobCtx, project.ID)
		require.ErrorIs(t, err, authz.ErrDenied)
		_, err = store.GetIdentityByID(bobCtx, project.ID)
		require.ErrorIs(t, err, authz.ErrDenied)
		_, err = store.GetDefaultAgentConfigID(bobCtx, project.ID)
		require.ErrorIs(t, err, authz.ErrDenied)
		_, err = store.GetManagedPermissions(bobCtx, project.ID)
		require.ErrorIs(t, err, authz.ErrDenied)
	}
	checkAbsent()
	for _, role := range []string{"viewer", "editor", "maintainer"} {
		require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, bob.UserID, role))
		_, err := store.GetByID(bobCtx, project.ID)
		require.NoError(t, err)
		err = store.Update(bobCtx, project)
		if role == "maintainer" {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, authz.ErrDenied)
		}
		called := false
		_, deleted, err := store.DeleteWithCleanupManifest(bobCtx, project.ID, func(persistence.TaskDeletionManifest) error { called = true; return nil })
		require.ErrorIs(t, err, authz.ErrDenied)
		require.False(t, deleted)
		require.False(t, called)
	}
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, bob.UserID, ""))
	checkAbsent()
	// Auth-off middleware supplies the same bootstrap owner principal.
	_, err = store.GetByID(ownerCtx, project.ID)
	require.NoError(t, err)
	require.NoError(t, store.Update(ownerCtx, project))
	_, deleted, err := store.DeleteWithCleanupManifest(ownerCtx, project.ID, nil)
	require.NoError(t, err)
	require.True(t, deleted)
}
