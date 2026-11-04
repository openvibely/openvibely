package sqlite

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestProjectWorkerLimitAuditRollbackAndRevocation(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	repo := NewProjectRepo(db)
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID, OrgID: models.DefaultOrgID})
	project := &models.Project{Name: "Worker limits"}
	require.NoError(t, repo.Create(owner, project))
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('limit-editor','limit-editor'); INSERT INTO org_memberships VALUES('default','limit-editor','member')`)
	require.NoError(t, err)
	access := NewProjectAccessRepo(db)
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, project.ID, "limit-editor", "maintainer"))
	actor := models.WithPrincipal(ctx, models.Principal{UserID: "limit-editor", Role: "owner"})
	limit := 3
	project.MaxWorkers = &limit
	require.NoError(t, repo.Update(actor, project))
	var details, who string
	require.NoError(t, db.QueryRow(`SELECT details,actor_user_id FROM project_audit_events WHERE project_id=? AND action='project.worker_limit'`, project.ID).Scan(&details, &who))
	require.JSONEq(t, `{"before":null,"after":3}`, details)
	require.Equal(t, "limit-editor", who)
	_, err = db.Exec(`CREATE TRIGGER fail_limit_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='project.worker_limit' BEGIN SELECT RAISE(ABORT,'audit failure'); END`)
	require.NoError(t, err)
	limit = 5
	require.ErrorContains(t, repo.Update(actor, project), "audit failure")
	stored, err := repo.GetByID(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, 3, *stored.MaxWorkers)
	_, err = db.Exec(`DROP TRIGGER fail_limit_audit`)
	require.NoError(t, err)
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, project.ID, "limit-editor", ""))
	require.ErrorIs(t, repo.Update(actor, project), persistence.ErrProjectDenied)
	stored, err = repo.GetByID(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, 3, *stored.MaxWorkers)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM project_audit_events WHERE project_id=? AND action='project.worker_limit'`, project.ID).Scan(&count))
	require.Equal(t, 1, count)
	project.MaxWorkers = nil
	require.NoError(t, repo.Update(owner, project))
	require.NoError(t, db.QueryRow(`SELECT details FROM project_audit_events WHERE project_id=? AND action='project.worker_limit' AND actor_user_id=?`, project.ID, models.BootstrapUserID).Scan(&details))
	require.JSONEq(t, `{"before":3,"after":null}`, details)
	require.NoError(t, repo.Update(owner, project))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM project_audit_events WHERE project_id=? AND action='project.worker_limit'`, project.ID).Scan(&count))
	require.Equal(t, 2, count, "unchanged limits do not duplicate events")
}
