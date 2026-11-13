package sqlite

import (
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestGitHubConnectionsArePrivateAndRequireCurrentMembership(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewGitHubConnectionRepo(db)
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('alice-git','alice-git'),('bob-git','bob-git'); INSERT INTO org_memberships VALUES('default','alice-git','member'),('default','bob-git','admin')`)
	require.NoError(t, err)
	alice := models.WithPrincipal(t.Context(), models.Principal{UserID: "alice-git"})
	bob := models.WithPrincipal(t.Context(), models.Principal{UserID: "bob-git", Role: "owner"})
	_, err = repo.GetOwn(t.Context())
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	saved, err := repo.SaveOwn(alice, models.GitHubConnection{PAT: "alice-secret", AuthorName: "Alice", AuthorEmail: "alice@example.com"}, 0)
	require.NoError(t, err)
	require.True(t, saved)
	c, err := repo.GetOwn(bob)
	require.NoError(t, err)
	require.Empty(t, c.PAT)
	c, err = repo.GetOwn(alice)
	require.NoError(t, err)
	require.Equal(t, "alice-secret", c.PAT)
	data, err := json.Marshal(c)
	require.NoError(t, err)
	require.NotContains(t, string(data), "alice-secret")
	_, err = db.Exec(`DELETE FROM org_memberships WHERE user_id='alice-git'`)
	require.NoError(t, err)
	_, err = repo.GetOwn(alice)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	_, err = repo.SaveOwn(alice, *c, c.Revision)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	_, err = repo.DeleteOwn(alice, c.Revision)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	_, err = db.Exec(`UPDATE users SET disabled=1 WHERE id='bob-git'`)
	require.NoError(t, err)
	_, err = repo.GetOwn(bob)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
}

func TestGitHubConnectionRevisionPreventsStaleSaveAfterDisconnect(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewGitHubConnectionRepo(db)
	ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	ok, err := repo.SaveOwn(ctx, models.GitHubConnection{PAT: "old-secret"}, 0)
	require.NoError(t, err)
	require.True(t, ok)
	c, err := repo.GetOwn(ctx)
	require.NoError(t, err)
	ok, err = repo.DeleteOwn(ctx, c.Revision)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = repo.SaveOwn(ctx, *c, c.Revision)
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = repo.SaveOwn(ctx, *c, 0)
	require.NoError(t, err)
	require.False(t, ok)
	after, err := repo.GetOwn(ctx)
	require.NoError(t, err)
	require.Empty(t, after.PAT)
	ok, err = repo.SaveOwn(ctx, models.GitHubConnection{PAT: "new-secret"}, after.Revision)
	require.NoError(t, err)
	require.True(t, ok)
	var details string
	rows, err := db.Query(`SELECT details FROM project_audit_events WHERE action LIKE 'github.connection.%'`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		require.NoError(t, rows.Scan(&details))
		require.NotContains(t, details, "secret")
	}
	require.NoError(t, rows.Err())
}
