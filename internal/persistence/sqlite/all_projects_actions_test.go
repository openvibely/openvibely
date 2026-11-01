package sqlite

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAllProjectsNativeBoardActions(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	repo := NewTaskRepo(db, nil)
	_, err := db.ExecContext(ctx, `INSERT INTO projects(id,name) VALUES ('second','Second')`)
	require.NoError(t, err)
	var tasks []*models.Task
	for _, owner := range []string{"default", "second"} {
		task := &models.Task{ProjectID: owner, Title: owner, Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
		require.NoError(t, repo.Create(ctx, task))
		tasks = append(tasks, task)
	}
	require.NoError(t, repo.ReorderTask(ctx, tasks[1].ID, 0, models.AllProjectsID))
	list, err := repo.ListByProject(ctx, models.AllProjectsID, string(models.CategoryBacklog))
	require.NoError(t, err)
	require.Equal(t, tasks[1].ID, list[0].ID)
	eligible, err := repo.ListBacklogByPriority(ctx, models.AllProjectsID, 0)
	require.NoError(t, err)
	require.Len(t, eligible, 2)
	count, err := repo.ActivateAllBacklog(ctx, models.AllProjectsID)
	require.NoError(t, err)
	require.Equal(t, 2, count)
	for _, task := range tasks {
		updated, err := repo.GetByID(ctx, task.ID)
		require.NoError(t, err)
		require.Equal(t, task.ProjectID, updated.ProjectID)
		require.Equal(t, models.CategoryActive, updated.Category)
		_, err = db.ExecContext(ctx, `UPDATE tasks SET status='completed' WHERE id=?`, task.ID)
		require.NoError(t, err)
	}
	count, err = repo.MoveCompletedActiveToCompleted(ctx, models.AllProjectsID)
	require.NoError(t, err)
	require.Equal(t, 2, count)
}

func TestAllProjectsAutomationBulkDelete(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	_, err := db.ExecContext(ctx, `INSERT INTO projects(id,name) VALUES ('second','Second')`)
	require.NoError(t, err)
	tasks, schedules, repo := NewTaskRepo(db, nil), NewScheduleRepo(db), NewAutomationRepo(db)
	first, _, _ := createOwnedTriggerAutomation(t, ctx, repo, tasks, schedules, "default", "one")
	second, _, _ := createOwnedTriggerAutomation(t, ctx, repo, tasks, schedules, "second", "two")
	require.Error(t, repo.DeleteAutomations(ctx, models.AllProjectsID, []string{first, "missing"}))
	definition, err := repo.GetDefinition(ctx, "default", first)
	require.NoError(t, err)
	require.NotNil(t, definition)
	require.NoError(t, repo.DeleteAutomations(ctx, models.AllProjectsID, []string{first, second}))
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automations WHERE id IN (?,?)`, first, second).Scan(&count))
	require.Zero(t, count)
}

func TestProjectReorderAfterAllProjects(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	repo := NewTaskRepo(db, nil)
	_, err := db.ExecContext(ctx, `INSERT INTO projects(id,name) VALUES ('second','Second')`)
	require.NoError(t, err)
	var tasks []*models.Task
	for i, owner := range []string{"default", "default", "second", "second"} {
		task := &models.Task{ProjectID: owner, Title: owner + string(rune('a'+i)), Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
		require.NoError(t, repo.Create(ctx, task))
		_, err = db.ExecContext(ctx, `UPDATE tasks SET display_order=? WHERE id=?`, i, task.ID)
		require.NoError(t, err)
		tasks = append(tasks, task)
	}
	require.NoError(t, repo.ReorderTask(ctx, tasks[2].ID, 0, models.AllProjectsID))
	require.NoError(t, repo.ReorderTask(ctx, tasks[0].ID, 1))
	list, err := repo.ListByProject(ctx, "default", string(models.CategoryBacklog))
	require.NoError(t, err)
	require.Equal(t, tasks[1].ID, list[0].ID, "moving first task down in its project must actually reorder it")
}

func TestRelativeProjectReorderAfterAllProjects(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	repo := NewTaskRepo(db, nil)
	_, err := db.ExecContext(ctx, `INSERT INTO projects(id,name) VALUES ('second','Second')`)
	require.NoError(t, err)
	var tasks []*models.Task
	for i, owner := range []string{"default", "second", "default", "second"} {
		task := &models.Task{ProjectID: owner, Title: owner + string(rune('a'+i)), Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
		require.NoError(t, repo.Create(ctx, task))
		_, err = db.ExecContext(ctx, `UPDATE tasks SET display_order=? WHERE id=?`, i*10, task.ID)
		require.NoError(t, err)
		tasks = append(tasks, task)
	}
	require.NoError(t, repo.ReorderTaskRelative(ctx, tasks[0].ID, tasks[3].ID, false, models.AllProjectsID))
	require.NoError(t, repo.ReorderTaskRelative(ctx, tasks[0].ID, tasks[2].ID, true, "default"))
	list, err := repo.ListByProject(ctx, "default", string(models.CategoryBacklog))
	require.NoError(t, err)
	require.Equal(t, tasks[0].ID, list[0].ID)
	require.NoError(t, repo.ReorderTaskRelative(ctx, tasks[0].ID, tasks[2].ID, false, "default"))
	list, err = repo.ListByProject(ctx, "default", string(models.CategoryBacklog))
	require.NoError(t, err)
	require.Equal(t, tasks[2].ID, list[0].ID)
	require.Error(t, repo.ReorderTaskRelative(ctx, tasks[0].ID, tasks[1].ID, true, "default"))
}
