package sqlite

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSlackUserProjectRepo_SetGetDelete(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewSlackUserProjectRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Slack Project"}
	require.NoError(t, projectRepo.Create(ctx, project))

	require.NoError(t, repo.SetUserProject(ctx, "T111", "U111", project.ID))

	got, err := repo.GetUserProject(ctx, "T111", "U111")
	require.NoError(t, err)
	require.Equal(t, project.ID, got)

	require.NoError(t, repo.DeleteUserProject(ctx, "T111", "U111"))

	got, err = repo.GetUserProject(ctx, "T111", "U111")
	require.NoError(t, err)
	require.Equal(t, "", got)
}

func TestSlackUserProjectRepo_Upsert(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewSlackUserProjectRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	p1 := &models.Project{Name: "Slack Project One"}
	require.NoError(t, projectRepo.Create(ctx, p1))
	p2 := &models.Project{Name: "Slack Project Two"}
	require.NoError(t, projectRepo.Create(ctx, p2))

	require.NoError(t, repo.SetUserProject(ctx, "T222", "U222", p1.ID))
	require.NoError(t, repo.SetUserProject(ctx, "T222", "U222", p2.ID))

	got, err := repo.GetUserProject(ctx, "T222", "U222")
	require.NoError(t, err)
	require.Equal(t, p2.ID, got)
}

func TestSlackSelectionRechecksBindingsAndAudits(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('sender','sender'); INSERT INTO org_memberships VALUES('default','sender','member')`)
	require.NoError(t, err)
	target := &models.Project{Name: "Target"}
	require.NoError(t, NewProjectRepo(db).Create(ctx, target))
	auth := NewSlackAuthRepo(db)
	repo := NewSlackUserProjectRepo(db)
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	sender := models.WithPrincipal(ctx, models.Principal{UserID: "sender"})
	sourceEntry := &models.SlackAuthorizedUser{ProjectID: "default", SlackUserID: "U123"}
	targetEntry := &models.SlackAuthorizedUser{ProjectID: target.ID, SlackUserID: "U123"}
	for _, entry := range []*models.SlackAuthorizedUser{sourceEntry, targetEntry} {
		require.NoError(t, auth.Create(owner, entry))
		require.NoError(t, auth.BindApplicationUser(owner, entry.ProjectID, entry.ID, "T123", "sender"))
	}
	admitted := models.WithSlackIngressIdentity(sender, models.SlackIngressIdentity{TeamID: "T123", ExternalUserID: "U123", ProjectID: "default", UserID: "sender"})
	require.NoError(t, repo.SetUserProject(ctx, "T123", "U123", "default"))
	unchanged := func() {
		t.Helper()
		selected, err := repo.GetUserProject(ctx, "T123", "U123")
		require.NoError(t, err)
		require.Equal(t, "default", selected)
	}
	require.Error(t, repo.SetUserProject(admitted, "OTHER", "U123", target.ID))
	unchanged()
	// The tool admitted the sender, but the target binding changes before write.
	require.NoError(t, auth.BindApplicationUser(owner, target.ID, targetEntry.ID, "T123", models.BootstrapUserID))
	require.Error(t, repo.SetUserProject(admitted, "T123", "U123", target.ID))
	unchanged()
	require.NoError(t, auth.BindApplicationUser(owner, target.ID, targetEntry.ID, "T123", "sender"))
	_, err = db.Exec(`UPDATE project_organizations SET org_role='viewer' WHERE project_id=?`, target.ID)
	require.NoError(t, err)
	require.Error(t, repo.SetUserProject(admitted, "T123", "U123", target.ID))
	unchanged()
	_, err = db.Exec(`UPDATE project_organizations SET org_role='editor' WHERE project_id=?`, target.ID)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TRIGGER fail_selection_audit BEFORE INSERT ON project_audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	require.Error(t, repo.SetUserProject(admitted, "T123", "U123", target.ID))
	unchanged()
	_, err = db.Exec(`DROP TRIGGER fail_selection_audit`)
	require.NoError(t, err)
	require.NoError(t, repo.SetUserProject(admitted, "T123", "U123", target.ID))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM project_audit_events WHERE action='slack.project_selected'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, auth.DeleteForProject(owner, "default", sourceEntry.ID))
	require.Error(t, repo.SetUserProject(admitted, "T123", "U123", target.ID))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM project_audit_events WHERE action='slack.project_selected'`).Scan(&count))
	require.Equal(t, 1, count)
}
