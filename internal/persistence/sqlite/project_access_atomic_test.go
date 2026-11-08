package sqlite

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestProjectAccessAuditFailureRollsBackPolicyAndMembership(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := &models.Project{Name: "Audit rollback"}
	require.NoError(t, NewProjectRepo(db).Create(ctx, project))
	repo := NewProjectAccessRepo(db)
	before, err := repo.Get(ctx, project.ID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO users(id,username) VALUES('bob','bob'); INSERT INTO org_memberships VALUES('default','bob','member'); CREATE TRIGGER fail_project_audit BEFORE INSERT ON project_audit_events BEGIN SELECT RAISE(ABORT,'injected audit failure'); END`)
	require.NoError(t, err)
	changed := *before
	changed.OrgRole = "viewer"
	require.ErrorContains(t, repo.Update(ctx, models.BootstrapUserID, changed), "injected audit failure")
	after, err := repo.Get(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.ErrorContains(t, repo.SetMember(ctx, models.BootstrapUserID, project.ID, "bob", "maintainer"), "injected audit failure")
	members, err := repo.Members(ctx, project.ID)
	require.NoError(t, err)
	require.Empty(t, members)
}
