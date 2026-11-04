package sqlite

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestDiscordPublicationProvenanceAndRollback(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	project := automationGraphTestProject(t, db, "Discord publisher")
	repo := NewAutomationRepo(db)
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	auth := NewDiscordAuthRepo(db)
	entry := &models.DiscordAuthorizedUser{ProjectID: project.ID, DiscordUserID: "123456789"}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, project.ID, entry.ID, models.BootstrapUserID))
	identity := models.DiscordIngressIdentity{ProjectID: project.ID, ExternalUserID: entry.DiscordUserID, UserID: models.BootstrapUserID}
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('discord-publisher','discord-publisher'); INSERT INTO org_memberships VALUES('default','discord-publisher','member')`)
	require.NoError(t, err)
	access := NewProjectAccessRepo(db)
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, project.ID, "discord-publisher", "maintainer"))
	require.NoError(t, auth.BindApplicationUser(owner, project.ID, entry.ID, "discord-publisher"))
	identity.UserID = "discord-publisher"
	actor := models.WithDiscordIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: identity.UserID, Role: "owner"}), identity)
	in := persistence.AutomationSaveWrite{ProjectID: project.ID, AutomationID: persistence.NewID(), GraphID: persistence.NewID(), Candidate: models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Discord publication", AutomationType: "custom", AdapterKey: "custom"}}
	_, err = db.Exec(`CREATE TRIGGER fail_discord_publisher BEFORE INSERT ON discord_publisher_provenance BEGIN SELECT RAISE(ABORT,'snapshot unavailable'); END`)
	require.NoError(t, err)
	_, _, err = repo.SaveCurrentGraph(actor, in)
	require.ErrorContains(t, err, "snapshot unavailable")
	require.Equal(t, 0, automationGraphCountRows(t, db, `SELECT count(*) FROM automation_versions WHERE id=?`, in.GraphID))
	require.Equal(t, 0, automationGraphCountRows(t, db, `SELECT count(*) FROM project_audit_events WHERE target_id=?`, in.AutomationID))
	_, err = db.Exec(`DROP TRIGGER fail_discord_publisher`)
	require.NoError(t, err)
	// Captured channel metadata without its application principal must fail closed.
	_, _, err = repo.SaveCurrentGraph(models.WithDiscordIngressIdentity(ctx, identity), in)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	_, _, err = repo.SaveCurrentGraph(actor, in)
	require.NoError(t, err)
	require.NoError(t, requireAutomationPublisher(ctx, db, project.ID, in.GraphID))
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, project.ID, identity.UserID, ""))
	require.ErrorIs(t, requireAutomationPublisher(owner, db, project.ID, in.GraphID), persistence.ErrProjectDenied, "owner caller cannot replace downgraded publisher")
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, project.ID, identity.UserID, "maintainer"))
	require.NoError(t, auth.BindApplicationUser(owner, project.ID, entry.ID, models.BootstrapUserID))
	require.ErrorIs(t, requireAutomationPublisher(owner, db, project.ID, in.GraphID), persistence.ErrProjectDenied, "rebound identity cannot replace publisher")
	require.NoError(t, auth.BindApplicationUser(owner, project.ID, entry.ID, identity.UserID))
	require.NoError(t, auth.DeleteForProject(owner, project.ID, entry.ID))
	require.ErrorIs(t, requireAutomationPublisher(owner, db, project.ID, in.GraphID), persistence.ErrProjectDenied)
	in.ExpectedCurrentGraphID = in.GraphID
	in.GraphID = persistence.NewID()
	_, _, err = repo.SaveCurrentGraph(actor, in)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	require.Equal(t, 0, automationGraphCountRows(t, db, `SELECT count(*) FROM automation_versions WHERE id=?`, in.GraphID))
}

func TestDiscordPublisherRevocationAcrossDispatchAndExecution(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
	tasks := NewTaskRepo(db, nil)
	task := createRuntimeScheduledTask(t, ctx, tasks, fixture.ProjectID, "Discord publisher dispatch")
	schedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, task.ID, fixture.Nodes["trigger"], time.Now().UTC().Add(-time.Minute))
	auth := NewDiscordAuthRepo(db)
	entry := &models.DiscordAuthorizedUser{ProjectID: fixture.ProjectID, DiscordUserID: "123456789"}
	bind := func() {
		require.NoError(t, auth.Create(owner, entry))
		require.NoError(t, auth.BindApplicationUser(owner, fixture.ProjectID, entry.ID, models.BootstrapUserID))
	}
	bind()
	publisher := models.WithDiscordIngressIdentity(owner, models.DiscordIngressIdentity{ProjectID: fixture.ProjectID, ExternalUserID: entry.DiscordUserID, UserID: models.BootstrapUserID})
	require.NoError(t, withImmediateTx(ctx, db, func(tx sqlExecutor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO automation_version_publishers(version_id,user_id) VALUES(?,?)`, fixture.VersionID, models.BootstrapUserID); err != nil {
			return err
		}
		return snapshotDiscordPublisher(publisher, tx, fixture.VersionID)
	}))
	repo := NewAutomationRepo(db)
	require.NoError(t, auth.DeleteForProject(owner, fixture.ProjectID, entry.ID))
	_, _, err := repo.ClaimScheduledOccurrence(owner, schedule, time.Now().UTC(), nil)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	bind()
	_, dispatch, err := repo.ClaimScheduledOccurrence(owner, schedule, time.Now().UTC(), nil)
	require.NoError(t, err)
	require.NotNil(t, dispatch)
	leased, err := repo.LeaseNextDispatch(ctx, "publisher", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.NoError(t, auth.DeleteForProject(owner, fixture.ProjectID, entry.ID))
	_, err = repo.GetDispatchEnvelope(owner, dispatch.ID)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	_, err = tasks.ClaimAutomationDispatch(owner, leased.ID, "publisher")
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	bind()
	execution, err := tasks.ClaimAutomationDispatch(owner, leased.ID, "publisher")
	require.NoError(t, err)
	identities := NewRunIdentityRepo(db)
	require.NoError(t, identities.CheckExecutionGoal(ctx, execution.ID, task.ID))
	require.NoError(t, auth.DeleteForProject(owner, fixture.ProjectID, entry.ID))
	require.ErrorIs(t, identities.CheckExecutionGoal(ctx, execution.ID, task.ID), persistence.ErrProjectDenied)
	_, err = db.Exec(`UPDATE executions SET status='queued' WHERE id=?`, execution.ID)
	require.NoError(t, err)
	require.ErrorIs(t, NewExecutionRepo(db).MarkRunning(owner, execution.ID), persistence.ErrProjectDenied)
	stored, err := NewExecutionRepo(db).GetByID(ctx, execution.ID)
	require.NoError(t, err)
	require.Equal(t, models.ExecQueued, stored.Status)
}
