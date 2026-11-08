package sqlite

import (
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestAutomationOccurrenceRechecksDiscordScheduleBinding(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
	task := createRuntimeScheduledTask(t, ctx, NewTaskRepo(db, nil), fixture.ProjectID, "Discord schedule identity")
	due := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	schedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, task.ID, fixture.Nodes["trigger"], due)
	auth := NewDiscordAuthRepo(db)
	entry := &models.DiscordAuthorizedUser{ProjectID: fixture.ProjectID, DiscordUserID: "123456789"}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, fixture.ProjectID, entry.ID, models.BootstrapUserID))
	admitted := models.WithDiscordIngressIdentity(owner, models.DiscordIngressIdentity{ExternalUserID: "123456789", ProjectID: fixture.ProjectID, UserID: models.BootstrapUserID})
	require.NoError(t, withImmediateTx(ctx, db, func(tx sqlExecutor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO schedule_run_identities(schedule_id,user_id) VALUES(?,?) ON CONFLICT(schedule_id) DO UPDATE SET user_id=excluded.user_id`, schedule.ID, models.BootstrapUserID); err != nil {
			return err
		}
		return snapshotDiscordSchedule(admitted, tx, schedule.ID)
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
	require.NoError(t, auth.BindApplicationUser(owner, fixture.ProjectID, entry.ID, models.BootstrapUserID))
	invocation, _, err := repo.ClaimScheduledOccurrence(owner, schedule, time.Now().UTC(), &next)
	require.NoError(t, err)
	require.NotNil(t, invocation)
	require.NoError(t, auth.DeleteForProject(owner, fixture.ProjectID, entry.ID))
	_, _, err = repo.ClaimScheduledOccurrence(ctx, schedule, time.Now().UTC(), &next)
	require.Error(t, err, "existing occurrence cannot bypass binding revocation")
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM automation_invocations WHERE automation_id=?`, fixture.AutomationID).Scan(&count))
	require.Equal(t, 1, count)
}

func TestDiscordInvocationBindingSurvivesDispatch(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
	tasks := NewTaskRepo(db, nil)
	task := createRuntimeScheduledTask(t, ctx, tasks, fixture.ProjectID, "Discord dispatch")
	schedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, task.ID, fixture.Nodes["trigger"], time.Now().UTC().Add(-time.Minute))
	auth := NewDiscordAuthRepo(db)
	entry := &models.DiscordAuthorizedUser{ProjectID: fixture.ProjectID, DiscordUserID: "987654321"}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, fixture.ProjectID, entry.ID, models.BootstrapUserID))
	admitted := models.WithDiscordIngressIdentity(owner, models.DiscordIngressIdentity{ExternalUserID: entry.DiscordUserID, ProjectID: fixture.ProjectID, UserID: models.BootstrapUserID})
	require.NoError(t, withImmediateTx(ctx, db, func(tx sqlExecutor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO schedule_run_identities(schedule_id,user_id) VALUES(?,?) ON CONFLICT(schedule_id) DO UPDATE SET user_id=excluded.user_id`, schedule.ID, models.BootstrapUserID)
		if err != nil {
			return err
		}
		return snapshotDiscordSchedule(admitted, tx, schedule.ID)
	}))
	repo := NewAutomationRepo(db)
	_, err := db.Exec(`CREATE TRIGGER reject_discord_invocation BEFORE INSERT ON discord_invocation_provenance BEGIN SELECT RAISE(ABORT,'provenance unavailable'); END`)
	require.NoError(t, err)
	_, _, err = repo.ClaimScheduledOccurrence(owner, schedule, time.Now().UTC(), nil)
	require.ErrorContains(t, err, "provenance unavailable")
	var admissions int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM automation_invocations WHERE automation_id=?`, fixture.AutomationID).Scan(&admissions))
	require.Zero(t, admissions, "snapshot failure rolls back invocation admission")
	_, err = db.Exec(`DROP TRIGGER reject_discord_invocation`)
	require.NoError(t, err)
	invocation, _, err := repo.ClaimScheduledOccurrence(owner, schedule, time.Now().UTC(), nil)
	require.NoError(t, err)
	require.NoError(t, requireDiscordInvocation(ctx, db, invocation.ID))
	// Schedule provenance is no longer needed: the invocation owns its snapshot.
	_, err = db.Exec(`DELETE FROM discord_schedule_provenance WHERE schedule_id=?`, schedule.ID)
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
	require.NoError(t, auth.BindApplicationUser(owner, fixture.ProjectID, entry.ID, models.BootstrapUserID))
	execution, err := tasks.ClaimAutomationDispatch(ctx, leased.ID, "lease")
	require.NoError(t, err)
	require.NoError(t, requireAutomationExecution(ctx, db, execution.ID))
	require.NoError(t, auth.DeleteForProject(owner, fixture.ProjectID, entry.ID))
	require.Error(t, requireAutomationExecution(ctx, db, execution.ID), "running/recovered execution rechecks saved invocation binding")
	_, err = tasks.ClaimAutomationDispatch(ctx, leased.ID, "lease")
	require.Error(t, err, "reuse cannot bypass removed binding")
}

func TestDiscordGeneratedSchedulePublication(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	project := automationGraphTestProject(t, db, "Schedule publisher")
	repo := NewAutomationRepo(db)
	actor := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	auth := NewDiscordAuthRepo(db)
	entry := &models.DiscordAuthorizedUser{ProjectID: project.ID, DiscordUserID: "123456789"}
	require.NoError(t, auth.Create(actor, entry))
	require.NoError(t, auth.BindApplicationUser(actor, project.ID, entry.ID, models.BootstrapUserID))
	identity := models.DiscordIngressIdentity{ProjectID: project.ID, ExternalUserID: entry.DiscordUserID, UserID: models.BootstrapUserID}
	actor = models.WithDiscordIngressIdentity(actor, identity)
	in := persistence.AutomationSaveWrite{ProjectID: project.ID, AutomationID: persistence.NewID(), GraphID: persistence.NewID(), Candidate: models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Scheduled", AutomationType: "custom", AdapterKey: "custom", Nodes: []models.AutomationDraftNode{
		{Key: "timer", Name: "Timer", Type: models.AutomationNodeTrigger, Role: "schedule", Config: map[string]any{"repeat_type": "daily"}},
		{Key: "task", Name: "Task", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "test"}},
	}}, Tasks: []persistence.AutomationSaveTask{{NodeKey: "task", Title: "Task", Prompt: "test", Category: models.CategoryScheduled, Priority: 2}}, Schedules: []persistence.AutomationSaveSchedule{{NodeKey: "timer", TaskNodeKey: "task", RunAt: time.Now(), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}}}
	_, _, denied := repo.SaveCurrentGraph(models.WithDiscordIngressIdentity(ctx, identity), in)
	require.ErrorIs(t, denied, persistence.ErrProjectDenied)
	_, err := db.Exec(`CREATE TRIGGER fail_scheduled_publication BEFORE INSERT ON project_audit_events WHEN NEW.action='automation.published' BEGIN SELECT RAISE(ABORT,'audit failure'); END`)
	require.NoError(t, err)
	_, _, err = repo.SaveCurrentGraph(actor, in)
	require.ErrorContains(t, err, "audit failure")
	require.Equal(t, 0, automationGraphCountRows(t, db, `SELECT COUNT(*) FROM schedule_run_identities r JOIN schedules s ON s.id=r.schedule_id JOIN tasks t ON t.id=s.task_id WHERE t.project_id=?`, project.ID))
	require.Equal(t, 0, automationGraphCountRows(t, db, `SELECT count(*) FROM discord_schedule_provenance`))
	_, err = db.Exec(`DROP TRIGGER fail_scheduled_publication`)
	require.NoError(t, err)
	_, _, err = repo.SaveCurrentGraph(actor, in)
	require.NoError(t, err)
	var scheduleID, taskID string
	require.NoError(t, db.QueryRow(`SELECT s.id,s.task_id FROM schedules s JOIN tasks t ON t.id=s.task_id WHERE t.project_id=?`, project.ID).Scan(&scheduleID, &taskID))
	recovered := NewRunIdentityRepo(db)
	user, err := recovered.ScheduleUser(ctx, scheduleID)
	require.NoError(t, err)
	require.Equal(t, models.BootstrapUserID, user)
	user, err = recovered.TaskUser(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, models.BootstrapUserID, user)
	// Republishing an existing schedule must not silently reassign its creator.
	_, err = db.Exec(`INSERT INTO users(id,username) VALUES('next-publisher','next-publisher'); INSERT INTO org_memberships VALUES('default','next-publisher','admin')`)
	require.NoError(t, err)
	in.ExpectedCurrentGraphID = in.GraphID
	in.GraphID = persistence.NewID()
	in.Tasks[0].ExistingTaskID = taskID
	in.Schedules[0].ExistingScheduleID = scheduleID
	_, _, err = repo.SaveCurrentGraph(models.WithPrincipal(ctx, models.Principal{UserID: "next-publisher"}), in)
	require.NoError(t, err)
	user, err = recovered.ScheduleUser(ctx, scheduleID)
	require.NoError(t, err)
	require.Equal(t, models.BootstrapUserID, user)
	require.NoError(t, auth.DeleteForProject(actor, project.ID, entry.ID))
	_, err = recovered.ScheduleUser(ctx, scheduleID)
	require.Error(t, err, "republishing must not erase the original schedule binding")

}
