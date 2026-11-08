package sqlite

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestIdentifiedExecutionAdmissionSnapshotAndRollback(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	task := createThreadInputTask(t, owner, db, "default")
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('executor','executor'); INSERT INTO org_memberships VALUES('default','executor','member'); INSERT INTO project_members VALUES('default','executor','editor');`)
	require.NoError(t, err)
	actor := models.WithPrincipal(ctx, models.Principal{UserID: "executor", Role: "owner"})
	repo := NewExecutionRepo(db)
	execution := models.Execution{TaskID: task.ID, Status: models.ExecQueued}
	require.NoError(t, repo.Create(actor, &execution))
	identities := NewRunIdentityRepo(db)
	require.NoError(t, identities.BindTask(owner, task.ID))
	saved, found, err := NewRunIdentityRepo(db).ExecutionUser(ctx, execution.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "executor", saved)
	var before int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM executions WHERE task_id=?`, task.ID).Scan(&before))
	_, err = db.Exec(`CREATE TRIGGER fail_execution_identity_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='execution.run_as' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	rejected := models.Execution{TaskID: task.ID, Status: models.ExecQueued}
	require.ErrorContains(t, repo.Create(actor, &rejected), "audit unavailable")
	var after int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM executions WHERE task_id=?`, task.ID).Scan(&after))
	require.Equal(t, before, after)
	_, found, err = identities.ExecutionUser(ctx, rejected.ID)
	require.NoError(t, err)
	require.False(t, found)
	_, err = db.Exec(`DROP TRIGGER fail_execution_identity_audit; UPDATE tasks SET created_via='webhook' WHERE id=?`, task.ID)
	require.NoError(t, err)
	rejected = models.Execution{TaskID: task.ID, Status: models.ExecQueued}
	require.ErrorIs(t, repo.Create(actor, &rejected), persistence.ErrProjectDenied)
	access := NewProjectAccessRepo(db)
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, "default", "executor", "maintainer"))
	require.NoError(t, repo.Create(actor, &rejected))
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, "default", "executor", ""))
	rejected = models.Execution{TaskID: task.ID, Status: models.ExecQueued}
	require.ErrorIs(t, repo.Create(actor, &rejected), persistence.ErrProjectDenied)
}

func TestDirectFollowupIdentityAndGoalRollback(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	task := createThreadInputTask(t, owner, db, "default")
	require.NoError(t, NewTaskRepo(db, nil).UpdateStatus(ctx, task.ID, models.StatusCompleted))
	goal := models.TaskGoal{TaskID: task.ID, GoalID: "direct-goal", Objective: "Finish"}
	require.NoError(t, NewTaskGoalRepo(db).CreateOrReplace(owner, &goal))
	input := models.ThreadInput{Content: "Continue", Source: models.TaskOriginSystemAgent, OriginAgent: models.AgentSystemKindGoal, GoalID: goal.GoalID}
	execution := models.Execution{TaskID: task.ID, IsFollowup: true}
	repo := NewExecutionRepo(db)
	_, err := db.Exec(`CREATE TRIGGER fail_direct_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='execution.run_as' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	started, err := repo.CreateDirectTaskFollowupOrQueue(owner, &execution, &input)
	require.ErrorContains(t, err, "audit unavailable")
	require.False(t, started)
	stored, err := NewTaskRepo(db, nil).GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusCompleted, stored.Status)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM executions WHERE task_id=?`, task.ID).Scan(&count))
	require.Zero(t, count)
	_, err = db.Exec(`DROP TRIGGER fail_direct_audit`)
	require.NoError(t, err)
	execution.ID = ""
	started, err = repo.CreateDirectTaskFollowupOrQueue(owner, &execution, &input)
	require.NoError(t, err)
	require.True(t, started)
	identities := NewRunIdentityRepo(db)
	require.NoError(t, identities.CheckExecutionGoal(ctx, execution.ID, task.ID))
	goal.GoalID = "replacement"
	require.NoError(t, NewTaskGoalRepo(db).CreateOrReplace(owner, &goal))
	require.ErrorIs(t, identities.CheckExecutionGoal(ctx, execution.ID, task.ID), persistence.ErrProjectDenied)
	require.ErrorIs(t, repo.MarkRunning(ctx, execution.ID), persistence.ErrProjectDenied)
	storedExecution, err := repo.GetByID(ctx, execution.ID)
	require.NoError(t, err)
	require.Equal(t, models.ExecQueued, storedExecution.Status)
	// Even the busy-task queue branch must reject a stale goal before writing.
	next := models.Execution{TaskID: task.ID, IsFollowup: true}
	started, err = repo.CreateDirectTaskFollowupOrQueue(owner, &next, &input)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	require.False(t, started)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM thread_inputs WHERE task_id=?`, task.ID).Scan(&count))
	require.Zero(t, count)
}

func TestDirectFollowupDenialLeavesQueueAndTaskUnchanged(t *testing.T) {
	for _, status := range []models.TaskStatus{models.StatusCompleted, models.StatusRunning} {
		t.Run(string(status), func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := t.Context()
			owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
			task := createThreadInputTask(t, owner, db, "default")
			require.NoError(t, NewTaskRepo(db, nil).UpdateStatus(ctx, task.ID, status))
			_, err := db.Exec(`INSERT INTO users(id,username) VALUES('viewer','viewer'); INSERT INTO org_memberships VALUES('default','viewer','member'); INSERT INTO project_members VALUES('default','viewer','viewer')`)
			require.NoError(t, err)
			actor := models.WithPrincipal(ctx, models.Principal{UserID: "viewer", Role: "owner"})
			execution := models.Execution{TaskID: task.ID, IsFollowup: true}
			input := models.ThreadInput{Content: "Denied"}
			started, err := NewExecutionRepo(db).CreateDirectTaskFollowupOrQueue(actor, &execution, &input)
			require.ErrorIs(t, err, persistence.ErrProjectDenied)
			require.False(t, started)
			stored, err := NewTaskRepo(db, nil).GetByID(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, status, stored.Status)
			for _, table := range []string{"executions", "thread_inputs"} {
				var count int
				require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE task_id=?`, task.ID).Scan(&count))
				require.Zero(t, count)
			}
		})
	}
}

func TestWebhookExecutionStartupRechecksSavedUser(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	task := createThreadInputTask(t, owner, db, "default")
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('hook-runner','hook-runner'); INSERT INTO org_memberships VALUES('default','hook-runner','member'); UPDATE tasks SET created_via='webhook' WHERE id=?`, task.ID)
	require.NoError(t, err)
	access := NewProjectAccessRepo(db)
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, "default", "hook-runner", "maintainer"))
	actor := models.WithPrincipal(ctx, models.Principal{UserID: "hook-runner"})
	repo := NewExecutionRepo(db)
	execution := models.Execution{TaskID: task.ID, Status: models.ExecQueued}
	require.NoError(t, repo.Create(actor, &execution))
	require.ErrorIs(t, repo.MarkRunning(owner, execution.ID), persistence.ErrProjectDenied, "another caller cannot borrow the saved user")
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, "default", "hook-runner", ""))
	require.ErrorIs(t, repo.MarkRunning(ctx, execution.ID), persistence.ErrProjectDenied, "recovery must check the saved user even without request context")
	stored, err := repo.GetByID(ctx, execution.ID)
	require.NoError(t, err)
	require.Equal(t, models.ExecQueued, stored.Status)
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, "default", "hook-runner", "maintainer"))
	require.NoError(t, repo.MarkRunning(ctx, execution.ID))
	stored, err = repo.GetByID(ctx, execution.ID)
	require.NoError(t, err)
	require.Equal(t, models.ExecRunning, stored.Status)
}
