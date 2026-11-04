package sqlite

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestAutomationPublicationPermissionAndAuditRollback(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	project := automationGraphTestProject(t, db, "Publication roles")
	repo := NewAutomationRepo(db)
	access := NewProjectAccessRepo(db)
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('publisher','publisher'); INSERT INTO org_memberships VALUES('default','publisher','member')`)
	require.NoError(t, err)
	actor := models.WithPrincipal(ctx, models.Principal{UserID: "publisher", Role: "owner"})
	in := persistence.AutomationSaveWrite{ProjectID: project.ID, AutomationID: persistence.NewID(), GraphID: persistence.NewID(), Candidate: models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Published", AutomationType: "custom", AdapterKey: "custom"}}
	_, _, err = repo.SaveCurrentGraph(actor, in)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, project.ID, "publisher", "maintainer"))
	_, err = db.Exec(`CREATE TRIGGER fail_publication_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='automation.published' BEGIN SELECT RAISE(ABORT,'audit failure'); END`)
	require.NoError(t, err)
	_, _, err = repo.SaveCurrentGraph(actor, in)
	require.ErrorContains(t, err, "audit failure")
	require.Equal(t, 0, automationGraphCountRows(t, db, `SELECT COUNT(*) FROM automations WHERE id=?`, in.AutomationID))
	require.Equal(t, 0, automationGraphCountRows(t, db, `SELECT COUNT(*) FROM automation_versions WHERE id=?`, in.GraphID))
	_, err = db.Exec(`DROP TRIGGER fail_publication_audit`)
	require.NoError(t, err)
	_, _, err = repo.SaveCurrentGraph(actor, in)
	require.NoError(t, err)
	var who, details string
	require.NoError(t, db.QueryRow(`SELECT actor_user_id,details FROM project_audit_events WHERE action='automation.published' AND target_id=?`, in.AutomationID).Scan(&who, &details))
	require.Equal(t, "publisher", who)
	require.Contains(t, details, in.GraphID)
	var publisher string
	require.NoError(t, db.QueryRow(`SELECT user_id FROM automation_version_publishers WHERE version_id=?`, in.GraphID).Scan(&publisher))
	require.Equal(t, "publisher", publisher)
	require.NoError(t, requireAutomationPublisher(ctx, db, project.ID, in.GraphID))
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, project.ID, "publisher", ""))
	require.ErrorIs(t, requireAutomationPublisher(ctx, db, project.ID, in.GraphID), persistence.ErrProjectDenied)
	in.AutomationID = persistence.NewID()
	in.GraphID = persistence.NewID()
	_, _, err = repo.SaveCurrentGraph(actor, in)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	require.Equal(t, 0, automationGraphCountRows(t, db, `SELECT COUNT(*) FROM automations WHERE id=?`, in.AutomationID))
}

func TestAutomationGeneratedScheduleRetainsPublisherIdentity(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	project := automationGraphTestProject(t, db, "Schedule publisher")
	repo := NewAutomationRepo(db)
	actor := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	in := persistence.AutomationSaveWrite{ProjectID: project.ID, AutomationID: persistence.NewID(), GraphID: persistence.NewID(), Candidate: models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Scheduled", AutomationType: "custom", AdapterKey: "custom", Nodes: []models.AutomationDraftNode{
		{Key: "timer", Name: "Timer", Type: models.AutomationNodeTrigger, Role: "schedule", Config: map[string]any{"repeat_type": "daily"}},
		{Key: "task", Name: "Task", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "test"}},
	}}, Tasks: []persistence.AutomationSaveTask{{NodeKey: "task", Title: "Task", Prompt: "test", Category: models.CategoryScheduled, Priority: 2}}, Schedules: []persistence.AutomationSaveSchedule{{NodeKey: "timer", TaskNodeKey: "task", RunAt: time.Now(), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}}}
	_, err := db.Exec(`CREATE TRIGGER fail_scheduled_publication BEFORE INSERT ON project_audit_events WHEN NEW.action='automation.published' BEGIN SELECT RAISE(ABORT,'audit failure'); END`)
	require.NoError(t, err)
	_, _, err = repo.SaveCurrentGraph(actor, in)
	require.ErrorContains(t, err, "audit failure")
	require.Equal(t, 0, automationGraphCountRows(t, db, `SELECT COUNT(*) FROM schedule_run_identities r JOIN schedules s ON s.id=r.schedule_id JOIN tasks t ON t.id=s.task_id WHERE t.project_id=?`, project.ID))
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
}

func TestSlackPublicationProvenanceAndRollback(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	project := automationGraphTestProject(t, db, "Slack publisher")
	repo := NewAutomationRepo(db)
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	auth := NewSlackAuthRepo(db)
	entry := &models.SlackAuthorizedUser{ProjectID: project.ID, SlackUserID: "publisher"}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, project.ID, entry.ID, "workspace", models.BootstrapUserID))
	identity := models.SlackIngressIdentity{ProjectID: project.ID, TeamID: "workspace", ExternalUserID: entry.SlackUserID, UserID: models.BootstrapUserID}
	actor := models.WithSlackIngressIdentity(owner, identity)
	in := persistence.AutomationSaveWrite{ProjectID: project.ID, AutomationID: persistence.NewID(), GraphID: persistence.NewID(), Candidate: models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Slack publication", AutomationType: "custom", AdapterKey: "custom"}}
	_, err := db.Exec(`CREATE TRIGGER fail_slack_publisher BEFORE INSERT ON slack_publisher_provenance BEGIN SELECT RAISE(ABORT,'snapshot unavailable'); END`)
	require.NoError(t, err)
	_, _, err = repo.SaveCurrentGraph(actor, in)
	require.ErrorContains(t, err, "snapshot unavailable")
	require.Equal(t, 0, automationGraphCountRows(t, db, `SELECT count(*) FROM automation_versions WHERE id=?`, in.GraphID))
	require.Equal(t, 0, automationGraphCountRows(t, db, `SELECT count(*) FROM project_audit_events WHERE target_id=?`, in.AutomationID))
	_, err = db.Exec(`DROP TRIGGER fail_slack_publisher`)
	require.NoError(t, err)
	// Captured channel metadata without its application principal must fail closed.
	_, _, err = repo.SaveCurrentGraph(models.WithSlackIngressIdentity(ctx, identity), in)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	_, _, err = repo.SaveCurrentGraph(actor, in)
	require.NoError(t, err)
	require.NoError(t, requireAutomationPublisher(ctx, db, project.ID, in.GraphID))
	require.NoError(t, auth.DeleteForProject(owner, project.ID, entry.ID))
	require.ErrorIs(t, requireAutomationPublisher(owner, db, project.ID, in.GraphID), persistence.ErrProjectDenied)
	in.ExpectedCurrentGraphID = in.GraphID
	in.GraphID = persistence.NewID()
	_, _, err = repo.SaveCurrentGraph(actor, in)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	require.Equal(t, 0, automationGraphCountRows(t, db, `SELECT count(*) FROM automation_versions WHERE id=?`, in.GraphID))
}

func TestSlackPublisherRevocationAcrossDispatchAndExecution(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
	tasks := NewTaskRepo(db, nil)
	task := createRuntimeScheduledTask(t, ctx, tasks, fixture.ProjectID, "Slack publisher dispatch")
	schedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, task.ID, fixture.Nodes["trigger"], time.Now().UTC().Add(-time.Minute))
	auth := NewSlackAuthRepo(db)
	entry := &models.SlackAuthorizedUser{ProjectID: fixture.ProjectID, SlackUserID: "publisher"}
	bind := func() {
		require.NoError(t, auth.Create(owner, entry))
		require.NoError(t, auth.BindApplicationUser(owner, fixture.ProjectID, entry.ID, "workspace", models.BootstrapUserID))
	}
	bind()
	publisher := models.WithSlackIngressIdentity(owner, models.SlackIngressIdentity{ProjectID: fixture.ProjectID, TeamID: "workspace", ExternalUserID: entry.SlackUserID, UserID: models.BootstrapUserID})
	require.NoError(t, withImmediateTx(ctx, db, func(tx sqlExecutor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO automation_version_publishers(version_id,user_id) VALUES(?,?)`, fixture.VersionID, models.BootstrapUserID); err != nil {
			return err
		}
		return snapshotSlackPublisher(publisher, tx, fixture.VersionID)
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
