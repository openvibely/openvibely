package sqlite

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestAggregateAccessMatchesProjectRoles(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('reader','reader'); INSERT INTO org_memberships VALUES('default','reader','member')`)
	require.NoError(t, err)
	projects := NewProjectRepo(db)
	access := NewProjectAccessRepo(db)
	reader := models.WithPrincipal(ctx, models.Principal{UserID: "reader", Role: "owner"}) // stale claims do not grant access
	project := &models.Project{Name: "Aggregate permissions"}
	require.NoError(t, projects.Create(ctx, project))
	policy, err := access.Get(ctx, project.ID)
	require.NoError(t, err)
	for _, visibility := range []string{"private", "org"} {
		for _, orgRole := range []string{"viewer", "editor"} {
			policy.Visibility = visibility
			policy.OrgRole = orgRole
			require.NoError(t, access.Update(ctx, models.BootstrapUserID, *policy))
			for _, grant := range []string{"", "viewer", "editor", "maintainer"} {
				require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, project.ID, "reader", grant))
				role, err := access.Role(ctx, "reader", project.ID)
				require.NoError(t, err)
				for _, edit := range []bool{false, true} {
					var allowed bool
					require.NoError(t, db.QueryRowContext(reader, `SELECT EXISTS(SELECT 1 FROM projects WHERE id=? AND `+accessibleProjectPredicate(reader, "id", edit)+`)`, project.ID).Scan(&allowed))
					want := role != ""
					if edit {
						want = role == "editor" || role == "maintainer" || role == "owner"
					}
					require.Equal(t, want, allowed, "visibility=%s orgRole=%s grant=%s edit=%v", visibility, orgRole, grant, edit)
				}
			}
		}
	}
	for _, mutation := range []string{`UPDATE users SET disabled=1 WHERE id='reader'`, `UPDATE users SET disabled=0 WHERE id='reader'; DELETE FROM org_memberships WHERE user_id='reader'`} {
		_, err = db.Exec(mutation)
		require.NoError(t, err)
		var count int
		require.NoError(t, db.QueryRowContext(reader, `SELECT COUNT(*) FROM projects WHERE `+accessibleProjectPredicate(reader, "id", false)).Scan(&count))
		require.Zero(t, count)
	}
}

func TestAggregateAlertsFilterBeforePaginationAndProtectBulkWrites(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('reader','reader'); INSERT INTO org_memberships VALUES('default','reader','member')`)
	require.NoError(t, err)
	reader := models.WithPrincipal(ctx, models.Principal{UserID: "reader"})
	projects := NewProjectRepo(db)
	access := NewProjectAccessRepo(db)
	alerts := NewAlertRepo(db)
	create := func(name, visibility, role string) *models.Alert {
		p := &models.Project{Name: name}
		require.NoError(t, projects.Create(ctx, p))
		policy, err := access.Get(ctx, p.ID)
		require.NoError(t, err)
		policy.Visibility = visibility
		policy.OrgRole = role
		require.NoError(t, access.Update(ctx, models.BootstrapUserID, *policy))
		a := &models.Alert{ProjectID: p.ID, Title: name, Type: models.AlertTaskFailed, Severity: models.SeverityError}
		require.NoError(t, alerts.Create(ctx, a))
		return a
	}
	editor := create("Editor", "org", "editor")
	viewer := create("Viewer", "org", "viewer")
	hidden := create("Hidden", "private", "viewer")
	// Make the inaccessible record sort first; pagination must still return a visible record.
	_, err = db.Exec(`UPDATE alerts SET created_at='2099-01-01 00:00:00' WHERE id=?`, hidden.ID)
	require.NoError(t, err)
	items, err := alerts.ListFiltered(reader, models.AllProjectsID, models.AlertListFilter{Limit: 1})
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NotEqual(t, hidden.ID, items[0].ID)
	require.Error(t, alerts.DeleteBulk(reader, models.AllProjectsID, []string{editor.ID, viewer.ID}))
	require.Error(t, alerts.MarkReadBulk(reader, models.AllProjectsID, []string{editor.ID, viewer.ID}))
	for _, a := range []*models.Alert{editor, viewer, hidden} {
		got, err := alerts.GetByIDForProject(ctx, a.ProjectID, a.ID)
		require.NoError(t, err)
		require.False(t, got.IsRead)
	}
	require.NoError(t, alerts.MarkAllRead(reader, models.AllProjectsID))
	got, err := alerts.GetByIDForProject(ctx, viewer.ProjectID, viewer.ID)
	require.NoError(t, err)
	require.False(t, got.IsRead)
	require.NoError(t, alerts.DeleteAll(reader, models.AllProjectsID))
	_, err = alerts.GetByIDForProject(ctx, editor.ProjectID, editor.ID)
	require.ErrorIs(t, err, persistence.ErrAlertNotFound)
	for _, a := range []*models.Alert{viewer, hidden} {
		_, err := alerts.GetByIDForProject(ctx, a.ProjectID, a.ID)
		require.NoError(t, err)
	}
}
