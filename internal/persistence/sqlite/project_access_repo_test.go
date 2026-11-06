package sqlite

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestProjectAccessRolesRevocationAndAudit(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('alice','alice'),('bob','bob'),('outsider','outsider'); INSERT INTO org_memberships VALUES('default','alice','member'),('default','bob','member')`)
	require.NoError(t, err)
	alice := models.WithPrincipal(ctx, models.Principal{UserID: "alice", OrgID: models.DefaultOrgID, Role: "member"})
	bob := models.WithPrincipal(ctx, models.Principal{UserID: "bob", OrgID: models.DefaultOrgID, Role: "owner"}) // Forged/stale claims must not grant a role.
	project := &models.Project{Name: "Alice project"}
	require.NoError(t, NewProjectRepo(db).Create(alice, project))
	repo := NewProjectAccessRepo(db)
	a := &authz.Authorizer{Store: repo}
	policy, err := repo.Get(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, "alice", policy.OwnerUserID)
	require.Equal(t, "org", policy.Visibility)
	require.NoError(t, a.Require(bob, project.ID, authz.Edit))
	require.ErrorIs(t, a.Require(bob, project.ID, authz.Manage), authz.ErrDenied)
	require.ErrorIs(t, a.Require(ctx, project.ID, authz.View), authz.ErrDenied)
	require.ErrorIs(t, a.Require(bob, project.ID, authz.Permission("unknown")), authz.ErrDenied)
	require.ErrorIs(t, repo.SetMember(ctx, "bob", project.ID, "bob", "maintainer"), persistence.ErrProjectDenied)

	// Private policies are constructed at the store layer for isolation tests;
	// the application service must keep private visibility gated until sandboxing.
	policy.Visibility = "private"
	require.NoError(t, repo.Update(ctx, "alice", *policy))
	require.ErrorIs(t, a.Require(bob, project.ID, authz.View), authz.ErrDenied)
	for _, role := range []string{"viewer", "editor", "maintainer"} {
		require.NoError(t, repo.SetMember(ctx, "alice", project.ID, "bob", role))
		for _, permission := range []authz.Permission{authz.View, authz.Edit, authz.Manage, authz.Own} {
			err := a.Require(bob, project.ID, permission)
			if authz.Rank(role) >= authz.Minimum(permission) {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, authz.ErrDenied)
			}
		}
	}
	// A maintainer cannot transfer ownership, even to themselves.
	transferred := *policy
	transferred.OwnerUserID = "bob"
	require.ErrorIs(t, repo.Update(ctx, "bob", transferred), persistence.ErrProjectDenied)
	require.ErrorIs(t, repo.SetMember(ctx, "alice", project.ID, "outsider", "viewer"), persistence.ErrProjectDenied)
	require.NoError(t, repo.SetMember(ctx, "alice", project.ID, "bob", ""))
	require.ErrorIs(t, a.Require(bob, project.ID, authz.View), authz.ErrDenied)
	require.NoError(t, repo.SetMember(ctx, "alice", project.ID, "bob", "editor"))
	_, err = db.Exec(`DELETE FROM org_memberships WHERE user_id='bob'`)
	require.NoError(t, err)
	require.ErrorIs(t, a.Require(bob, project.ID, authz.View), authz.ErrDenied)
	members, err := repo.Members(ctx, project.ID)
	require.NoError(t, err)
	require.Empty(t, members)
	events, err := repo.Audit(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, events, 6)
	require.Equal(t, "project.policy", events[5].Action)
	require.Contains(t, events[5].Details, `"org_id":"default"`)
}

func TestProjectAccessDisabledOwnerAndDeletedProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewProjectAccessRepo(db)
	project := &models.Project{Name: "Disabled owner"}
	require.NoError(t, NewProjectRepo(db).Create(ctx, project))
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	a := &authz.Authorizer{Store: repo}
	require.NoError(t, a.Require(owner, project.ID, authz.Own))
	_, err := db.Exec(`UPDATE users SET disabled=1 WHERE id=?`, models.BootstrapUserID)
	require.NoError(t, err)
	require.ErrorIs(t, a.Require(owner, project.ID, authz.View), authz.ErrDenied)
	_, err = db.Exec(`UPDATE users SET disabled=0 WHERE id=?`, models.BootstrapUserID)
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM projects WHERE id=?`, project.ID)
	require.NoError(t, err)
	require.ErrorIs(t, a.Require(owner, project.ID, authz.View), authz.ErrDenied)
}

func TestProjectCreateRejectsRemovedPrincipalAtomically(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := models.WithPrincipal(context.Background(), models.Principal{UserID: "missing", OrgID: models.DefaultOrgID})
	project := &models.Project{Name: "Rejected"}
	require.ErrorIs(t, NewProjectRepo(db).Create(ctx, project), persistence.ErrOrgDenied)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM projects WHERE name='Rejected'`).Scan(&count))
	require.Zero(t, count)
}

func TestOrgAdminProjectOverrideIsAudited(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('alice','alice'),('admin','admin'); INSERT INTO org_memberships VALUES('default','alice','member'),('default','admin','admin')`)
	require.NoError(t, err)
	alice := models.WithPrincipal(ctx, models.Principal{UserID: "alice", OrgID: models.DefaultOrgID})
	admin := models.WithPrincipal(ctx, models.Principal{UserID: "admin", OrgID: models.DefaultOrgID})
	projects, access := NewProjectRepo(db), NewProjectAccessRepo(db)

	owned := &models.Project{Name: "Owner transfer"}
	require.NoError(t, projects.Create(alice, owned))
	policy, err := access.Get(ctx, owned.ID)
	require.NoError(t, err)
	policy.OwnerUserID = "admin"
	require.NoError(t, access.Update(ctx, "alice", *policy))
	events, err := access.Audit(ctx, owned.ID)
	require.NoError(t, err)
	require.NotContains(t, events[0].Details, "org_admin_override")

	other := &models.Project{Name: "Admin transfer"}
	require.NoError(t, projects.Create(alice, other))
	policy, err = access.Get(ctx, other.ID)
	require.NoError(t, err)
	policy.OwnerUserID = "admin"
	require.NoError(t, access.Update(ctx, "admin", *policy))
	events, err = access.Audit(ctx, other.ID)
	require.NoError(t, err)
	require.Equal(t, "project.policy", events[0].Action)
	require.Contains(t, events[0].Details, `"org_admin_override":true`)

	deleted := &models.Project{Name: "Admin delete"}
	require.NoError(t, projects.Create(alice, deleted))
	_, ok, err := projects.DeleteWithCleanupManifest(admin, deleted.ID, nil)
	require.NoError(t, err)
	require.True(t, ok)
	var details string
	require.NoError(t, db.QueryRow(`SELECT details FROM project_audit_events WHERE project_id=? AND action='project.deleted'`, deleted.ID).Scan(&details))
	require.Equal(t, `{"org_admin_override":true}`, details)

	ownDelete := &models.Project{Name: "Owner delete"}
	require.NoError(t, projects.Create(alice, ownDelete))
	_, ok, err = projects.DeleteWithCleanupManifest(alice, ownDelete.ID, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, db.QueryRow(`SELECT details FROM project_audit_events WHERE project_id=? AND action='project.deleted'`, ownDelete.ID).Scan(&details))
	require.Empty(t, details)
}
