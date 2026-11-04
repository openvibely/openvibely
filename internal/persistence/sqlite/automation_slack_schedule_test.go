package sqlite

import (
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestAutomationOccurrenceRechecksSlackScheduleBinding(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
	task := createRuntimeScheduledTask(t, ctx, NewTaskRepo(db, nil), fixture.ProjectID, "Slack schedule identity")
	due := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	schedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, task.ID, fixture.Nodes["trigger"], due)
	auth := NewSlackAuthRepo(db)
	entry := &models.SlackAuthorizedUser{ProjectID: fixture.ProjectID, SlackUserID: "U123"}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, fixture.ProjectID, entry.ID, "T123", models.BootstrapUserID))
	admitted := models.WithSlackIngressIdentity(owner, models.SlackIngressIdentity{TeamID: "T123", ExternalUserID: "U123", ProjectID: fixture.ProjectID, UserID: models.BootstrapUserID})
	require.NoError(t, withImmediateTx(ctx, db, func(tx sqlExecutor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO schedule_run_identities(schedule_id,user_id) VALUES(?,?) ON CONFLICT(schedule_id) DO UPDATE SET user_id=excluded.user_id`, schedule.ID, models.BootstrapUserID); err != nil {
			return err
		}
		return snapshotSlackSchedule(admitted, tx, schedule.ID)
	}))
	require.NoError(t, auth.DeleteForProject(owner, fixture.ProjectID, entry.ID))
	repo := NewAutomationRepo(db)
	next := time.Now().UTC().Add(time.Hour)
	_, _, err := repo.ClaimScheduledOccurrence(ctx, schedule, time.Now().UTC(), &next)
	require.Error(t, err, "recovery without context must recheck the saved binding")
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM automation_invocations WHERE automation_id=?`, fixture.AutomationID).Scan(&count))
	require.Zero(t, count)
	var savedDue time.Time
	require.NoError(t, db.QueryRow(`SELECT next_run FROM schedules WHERE id=?`, schedule.ID).Scan(&savedDue))
	require.True(t, due.Equal(savedDue))
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, fixture.ProjectID, entry.ID, "T123", models.BootstrapUserID))
	invocation, _, err := repo.ClaimScheduledOccurrence(owner, schedule, time.Now().UTC(), &next)
	require.NoError(t, err)
	require.NotNil(t, invocation)
	require.NoError(t, auth.DeleteForProject(owner, fixture.ProjectID, entry.ID))
	_, _, err = repo.ClaimScheduledOccurrence(ctx, schedule, time.Now().UTC(), &next)
	require.Error(t, err, "existing occurrence cannot bypass binding revocation")
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM automation_invocations WHERE automation_id=?`, fixture.AutomationID).Scan(&count))
	require.Equal(t, 1, count)
}

func TestSlackInvocationBindingSurvivesDispatch(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
	tasks := NewTaskRepo(db, nil)
	task := createRuntimeScheduledTask(t, ctx, tasks, fixture.ProjectID, "Slack dispatch")
	schedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, task.ID, fixture.Nodes["trigger"], time.Now().UTC().Add(-time.Minute))
	auth := NewSlackAuthRepo(db)
	entry := &models.SlackAuthorizedUser{ProjectID: fixture.ProjectID, SlackUserID: "dispatch-sender"}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, fixture.ProjectID, entry.ID, "workspace", models.BootstrapUserID))
	admitted := models.WithSlackIngressIdentity(owner, models.SlackIngressIdentity{TeamID: "workspace", ExternalUserID: entry.SlackUserID, ProjectID: fixture.ProjectID, UserID: models.BootstrapUserID})
	require.NoError(t, withImmediateTx(ctx, db, func(tx sqlExecutor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO schedule_run_identities(schedule_id,user_id) VALUES(?,?) ON CONFLICT(schedule_id) DO UPDATE SET user_id=excluded.user_id`, schedule.ID, models.BootstrapUserID)
		if err != nil {
			return err
		}
		return snapshotSlackSchedule(admitted, tx, schedule.ID)
	}))
	repo := NewAutomationRepo(db)
	_, err := db.Exec(`CREATE TRIGGER reject_slack_invocation BEFORE INSERT ON slack_invocation_provenance BEGIN SELECT RAISE(ABORT,'provenance unavailable'); END`)
	require.NoError(t, err)
	_, _, err = repo.ClaimScheduledOccurrence(owner, schedule, time.Now().UTC(), nil)
	require.ErrorContains(t, err, "provenance unavailable")
	var admissions int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM automation_invocations WHERE automation_id=?`, fixture.AutomationID).Scan(&admissions))
	require.Zero(t, admissions, "snapshot failure rolls back invocation admission")
	_, err = db.Exec(`DROP TRIGGER reject_slack_invocation`)
	require.NoError(t, err)
	invocation, _, err := repo.ClaimScheduledOccurrence(owner, schedule, time.Now().UTC(), nil)
	require.NoError(t, err)
	require.NoError(t, requireSlackInvocation(ctx, db, invocation.ID))
	// Schedule provenance is no longer needed: the invocation owns its snapshot.
	_, err = db.Exec(`DELETE FROM slack_schedule_provenance WHERE schedule_id=?`, schedule.ID)
	require.NoError(t, err)
	leased, err := repo.LeaseNextDispatch(ctx, "lease", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, leased)
	require.NoError(t, auth.DeleteForProject(owner, fixture.ProjectID, entry.ID))
	_, err = tasks.ClaimAutomationDispatch(owner, leased.ID, "lease")
	require.Error(t, err)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM executions WHERE dispatch_id=?`, leased.ID).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, fixture.ProjectID, entry.ID, "workspace", models.BootstrapUserID))
	execution, err := tasks.ClaimAutomationDispatch(ctx, leased.ID, "lease")
	require.NoError(t, err)
	require.NoError(t, requireAutomationExecution(ctx, db, execution.ID))
	require.NoError(t, auth.DeleteForProject(owner, fixture.ProjectID, entry.ID))
	require.Error(t, requireAutomationExecution(ctx, db, execution.ID), "running/recovered execution rechecks saved invocation binding")
	_, err = tasks.ClaimAutomationDispatch(ctx, leased.ID, "lease")
	require.Error(t, err, "reuse cannot bypass removed binding")
}
