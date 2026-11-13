package sqlite

import (
	"sync"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestGitHubConnectionAuditFailureRollsBackCredentialWrite(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewGitHubConnectionRepo(db)
	ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	ok, err := repo.SaveOwn(ctx, models.GitHubConnection{PAT: "initial"}, 0)
	require.NoError(t, err)
	require.True(t, ok)
	c, err := repo.GetOwn(ctx)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TRIGGER reject_github_audit BEFORE INSERT ON project_audit_events WHEN NEW.action LIKE 'github.connection.%' BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`)
	require.NoError(t, err)
	ok, err = repo.SaveOwn(ctx, models.GitHubConnection{PAT: "replacement"}, c.Revision)
	require.Error(t, err)
	require.False(t, ok)
	ok, err = repo.DeleteOwn(ctx, c.Revision)
	require.Error(t, err)
	require.False(t, ok)
	actual, err := repo.GetOwn(ctx)
	require.NoError(t, err)
	require.Equal(t, c, actual)
}

func TestGitHubConnectionConcurrentSavesDoNotOverwriteEachOther(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewGitHubConnectionRepo(db)
	ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	type result struct {
		changed bool
		err     error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, token := range []string{"first", "second"} {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			ok, err := repo.SaveOwn(ctx, models.GitHubConnection{PAT: token}, 0)
			results <- result{ok, err}
		}(token)
	}
	wg.Wait()
	close(results)
	writes := 0
	for result := range results {
		require.NoError(t, result.err)
		if result.changed {
			writes++
		}
	}
	require.Equal(t, 1, writes)
	c, err := repo.GetOwn(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, c.Revision)
	require.Contains(t, []string{"first", "second"}, c.PAT)
}
