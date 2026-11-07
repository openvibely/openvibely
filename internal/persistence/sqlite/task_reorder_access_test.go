package sqlite

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestTaskReorderRespectsCurrentProjectAccess(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('reorder-user','reorder-user'); INSERT INTO org_memberships VALUES('default','reorder-user','member')`)
	require.NoError(t, err)
	user := models.WithPrincipal(ctx, models.Principal{UserID: "reorder-user", Role: "owner"})
	projects := NewProjectRepo(db)
	access := NewProjectAccessRepo(db)
	tasks := NewTaskRepo(db, nil)
	create := func(name, visibility, role string) *models.Project {
		p := &models.Project{Name: name}
		require.NoError(t, projects.Create(ctx, p))
		policy, err := access.Get(ctx, p.ID)
		require.NoError(t, err)
		policy.Visibility = visibility
		policy.OrgRole = role
		require.NoError(t, access.Update(ctx, models.BootstrapUserID, *policy))
		return p
	}
	editable := create("Editable", "org", "editor")
	viewer := create("Viewer", "org", "viewer")
	hidden := create("Hidden", "private", "viewer")
	makeTask := func(p *models.Project, title string) *models.Task {
		task := &models.Task{ProjectID: p.ID, Title: title, Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
		require.NoError(t, tasks.Create(ctx, task))
		return task
	}
	first := makeTask(editable, "First")
	second := makeTask(editable, "Second")
	protected := []*models.Task{makeTask(viewer, "Read only"), makeTask(hidden, "Hidden")}
	// Explicit sparse positions make unauthorized normalization observable.
	for i, task := range append([]*models.Task{first, second}, protected...) {
		_, err := db.Exec(`UPDATE tasks SET display_order=?,updated_at='2000-01-01 00:00:00' WHERE id=?`, 10+i*10, task.ID)
		require.NoError(t, err)
	}
	snapshot := func() map[string]models.Task {
		out := map[string]models.Task{}
		for _, task := range append([]*models.Task{first, second}, protected...) {
			row, err := tasks.GetByID(ctx, task.ID)
			require.NoError(t, err)
			out[row.ID] = *row
		}
		return out
	}
	original := snapshot()
	for _, task := range protected {
		require.ErrorIs(t, tasks.ReorderTask(user, task.ID, 0, models.AllProjectsID), persistence.ErrProjectDenied)
		require.ErrorIs(t, tasks.ReorderTaskRelative(user, task.ID, first.ID, true, models.AllProjectsID), persistence.ErrProjectDenied)
		require.Error(t, tasks.ReorderTaskRelative(user, first.ID, task.ID, true, models.AllProjectsID))
	}
	require.Equal(t, original, snapshot(), "denied moves and anchors must not modify any task")
	require.NoError(t, tasks.ReorderTaskRelative(user, second.ID, first.ID, true, models.AllProjectsID))
	after := snapshot()
	require.Less(t, after[second.ID].DisplayOrder, after[first.ID].DisplayOrder)
	for _, task := range protected {
		require.Equal(t, original[task.ID], after[task.ID])
	}
	// Permission changes must take effect even when the caller still has stale claims.
	policy, err := access.Get(ctx, editable.ID)
	require.NoError(t, err)
	policy.OrgRole = "viewer"
	require.NoError(t, access.Update(ctx, models.BootstrapUserID, *policy))
	require.ErrorIs(t, tasks.ReorderTask(user, first.ID, 0, models.AllProjectsID), persistence.ErrProjectDenied)
	require.Equal(t, after, snapshot())
}
