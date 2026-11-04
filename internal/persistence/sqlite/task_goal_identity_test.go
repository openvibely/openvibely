package sqlite

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestGoalIdentityAuditRollback(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	task := models.Task{ProjectID: "default", Title: "Goal atomicity", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, NewTaskRepo(db, nil).Create(ctx, &task))
	repo := NewTaskGoalRepo(db)
	original := models.TaskGoal{TaskID: task.ID, GoalID: "original", Objective: "Finish"}
	require.NoError(t, repo.CreateOrReplace(ctx, &original))
	_, err := db.Exec(`CREATE TRIGGER reject_goal_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='goal.run_as' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	replacement := models.TaskGoal{TaskID: task.ID, GoalID: "replacement", Objective: "New goal"}
	require.Error(t, repo.CreateOrReplace(ctx, &replacement))
	stored, err := repo.GetByTaskID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "original", stored.GoalID)
	require.Equal(t, "Finish", stored.Objective)
	require.Equal(t, models.BootstrapUserID, stored.RunAsUserID)
}

func TestTaskCreationCapturesGoalIdentity(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	task := models.Task{ProjectID: "default", Title: "Task plus goal", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	goal := models.TaskGoal{Objective: "Finish"}
	require.NoError(t, NewTaskRepo(db, nil).CreateWithGoal(ctx, &task, &goal))
	stored, err := NewTaskGoalRepo(db).GetByTaskID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.BootstrapUserID, stored.RunAsUserID)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM project_audit_events WHERE action='goal.run_as' AND target_id=?`, stored.GoalID).Scan(&count))
	require.Equal(t, 1, count)
}
