package sqlite

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestGitHubReviewerManagementAuthorizationAndAudit(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('manager','manager'); INSERT INTO org_memberships VALUES('default','manager','admin')`)
	require.NoError(t, err)
	principal := models.WithPrincipal(ctx, models.Principal{UserID: "manager", Role: "owner"})
	repo := NewGitHubAuthRepo(db)
	actor := &models.GitHubAuthorizedActor{GitHubLogin: "@Reviewer", Permission: "triage", AddedBy: "forged attribution"}
	require.NoError(t, repo.UpsertAuthorizedActor(principal, actor))
	require.Equal(t, "manager", actor.AddedBy)
	id := actor.ID
	_, err = db.Exec(`UPDATE org_memberships SET role='member' WHERE user_id='manager'`)
	require.NoError(t, err)
	actor.Permission = "admin"
	require.ErrorIs(t, repo.UpsertAuthorizedActor(principal, actor), persistence.ErrOrgDenied)
	require.ErrorIs(t, repo.DeleteAuthorizedActor(principal, id), persistence.ErrOrgDenied)
	actors, err := repo.ListAuthorizedActors(ctx)
	require.NoError(t, err)
	require.Len(t, actors, 1)
	require.Equal(t, "triage", actors[0].Permission)
	_, err = db.Exec(`UPDATE org_memberships SET role='admin' WHERE user_id='manager'; CREATE TRIGGER reject_reviewer_audit BEFORE INSERT ON project_audit_events WHEN NEW.action IN ('github.reviewer_saved','github.reviewer_removed') BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, repo.UpsertAuthorizedActor(principal, actor), "audit unavailable")
	require.ErrorContains(t, repo.DeleteAuthorizedActor(principal, id), "audit unavailable")
	require.ErrorContains(t, repo.UpsertAuthorizedActor(principal, &models.GitHubAuthorizedActor{GitHubLogin: "new-reviewer"}), "audit unavailable")
	actors, err = repo.ListAuthorizedActors(ctx)
	require.NoError(t, err)
	require.Len(t, actors, 1)
	require.Equal(t, "triage", actors[0].Permission)
	_, err = db.Exec(`DROP TRIGGER reject_reviewer_audit`)
	require.NoError(t, err)
	require.NoError(t, repo.UpsertAuthorizedActor(principal, actor))
	require.Equal(t, id, actor.ID, "upsert must preserve reviewer identity")
	require.NoError(t, repo.DeleteAuthorizedActor(principal, id))
	var audits int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM project_audit_events WHERE actor_user_id='manager' AND action IN ('github.reviewer_saved','github.reviewer_removed')`).Scan(&audits))
	require.Equal(t, 3, audits)
	_, err = db.Exec(`UPDATE users SET disabled=1 WHERE id='manager'`)
	require.NoError(t, err)
	require.ErrorIs(t, repo.UpsertAuthorizedActor(principal, actor), persistence.ErrOrgDenied)
	// Unconfigured embedded repositories retain their legacy caller contract.
	require.NoError(t, repo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "embedded"}))
}
