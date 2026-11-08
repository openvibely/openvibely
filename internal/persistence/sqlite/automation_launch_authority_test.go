package sqlite

import (
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestArchivedLaunchAuthorityRechecksRevocation(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
	task := createRuntimeScheduledTask(t, ctx, NewTaskRepo(db, nil), fixture.ProjectID, "Archived launch")
	createRuntimeAutomationSchedule(t, ctx, db, fixture, task.ID, fixture.Nodes["trigger"], time.Now().Add(time.Hour))
	auth := NewSlackAuthRepo(db)
	entry := &models.SlackAuthorizedUser{ProjectID: fixture.ProjectID, SlackUserID: "sender"}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, fixture.ProjectID, entry.ID, "workspace", models.BootstrapUserID))
	actor := models.WithSlackIngressIdentity(owner, models.SlackIngressIdentity{ProjectID: fixture.ProjectID, TeamID: "workspace", ExternalUserID: entry.SlackUserID, UserID: models.BootstrapUserID})
	invocations, _, err := NewAutomationRepo(db).ClaimManualAutomationRun(actor, fixture.ProjectID, fixture.AutomationID, time.Now())
	require.NoError(t, err)
	require.Len(t, invocations, 1)
	b := models.AutomationBinding{AutomationID: fixture.AutomationID, VersionID: fixture.VersionID, NodeID: fixture.Nodes["trigger"], InvocationID: invocations[0].ID}
	require.Error(t, requireArchivedLaunchAuthority(ctx, db, fixture.ProjectID, b), "no inferred archive")
	// Archiving and graph deletion must commit together; failed replacement
	// must leave the live graph and no partial archive.
	_, err = db.Exec(`CREATE TRIGGER reject_archived_graph_delete BEFORE DELETE ON automation_versions BEGIN SELECT RAISE(ABORT, 'replacement failed'); END`)
	require.NoError(t, err)
	require.Error(t, withImmediateTx(ctx, db, func(tx sqlExecutor) error {
		if err := archiveAutomationLaunchAuthority(ctx, tx, fixture.ProjectID, fixture.AutomationID, "replacement"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM automation_versions WHERE id=?`, fixture.VersionID)
		return err
	}))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM automation_launch_authority`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM automation_versions WHERE id=?`, fixture.VersionID).Scan(&count))
	require.Equal(t, 1, count)
	_, err = db.Exec(`DROP TRIGGER reject_archived_graph_delete`)
	require.NoError(t, err)
	require.NoError(t, withImmediateTx(ctx, db, func(tx sqlExecutor) error {
		if err := archiveAutomationLaunchAuthority(ctx, tx, fixture.ProjectID, fixture.AutomationID, "replacement"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM automation_versions WHERE id=?`, fixture.VersionID)
		return err
	}))
	source := persistence.AutomationGitHubIssueDedupSource{Context: models.AutomationContext{ProjectID: fixture.ProjectID, Bindings: []models.AutomationBinding{b}}}
	require.NoError(t, authorizeGitHubIssueDedupSource(owner, db, fixture.ProjectID, source))
	wrong := b
	wrong.NodeID = "unknown"
	require.Error(t, requireArchivedLaunchAuthority(owner, db, fixture.ProjectID, wrong))
	require.Error(t, requireArchivedLaunchAuthority(owner, db, "default", b))
	require.NoError(t, auth.DeleteForProject(owner, fixture.ProjectID, entry.ID))
	require.ErrorIs(t, authorizeGitHubIssueDedupSource(owner, db, fixture.ProjectID, source), persistence.ErrProjectDenied)
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, fixture.ProjectID, entry.ID, "workspace", models.BootstrapUserID))
	require.NoError(t, authorizeGitHubIssueDedupSource(owner, db, fixture.ProjectID, source))
	_, err = db.Exec(`UPDATE users SET disabled=1 WHERE id=?`, models.BootstrapUserID)
	require.NoError(t, err)
	require.ErrorIs(t, authorizeGitHubIssueDedupSource(ctx, db, fixture.ProjectID, source), persistence.ErrProjectDenied, "archived user must remain enabled")
}
