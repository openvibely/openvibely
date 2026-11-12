package sqlite

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestExecutionDiffRetention(t *testing.T) {
	for _, tied := range []bool{false, true} {
		t.Run(fmt.Sprintf("tied=%t", tied), func(t *testing.T) {
			db := testutil.NewTestDB(t)
			repo := NewExecutionRepo(db)
			ctx := context.Background()
			_, err := db.Exec(`INSERT INTO tasks(id,project_id,title) VALUES('t','default','T'),('other','default','Other');
 INSERT INTO executions(id,task_id,started_at,prompt_sent) VALUES
 ('old','t','2026-01-01','old prompt'),('new','t','2026-01-02','new prompt'),('other','other','2026-01-01','other prompt')`)
			require.NoError(t, err)
			if tied {
				_, err = db.Exec(`UPDATE executions SET started_at='2026-01-01'`)
				require.NoError(t, err)
			}
			assertDiff := func(want string) {
				t.Helper()
				got, err := repo.GetLatestNonEmptyDiffOutput(ctx, "t")
				require.NoError(t, err)
				require.Equal(t, want, got)
				var n int
				require.NoError(t, db.QueryRow(`SELECT count(*) FROM executions WHERE task_id='t' AND diff_output != ''`).Scan(&n))
				require.Equal(t, 1, n)
			}
			require.NoError(t, repo.UpdateDiffOutput(ctx, "other", "other diff"))
			require.NoError(t, repo.UpdateDiffOutput(ctx, "old", "first"))
			require.NoError(t, repo.UpdateDiffOutput(ctx, "old", "updated"))
			assertDiff("updated")
			require.NoError(t, repo.UpdateDiffOutput(ctx, "old", ""))
			require.NoError(t, repo.UpdateDiffOutput(ctx, "new", ""))
			assertDiff("updated")
			require.NoError(t, repo.UpdateDiffOutput(ctx, "new", "new diff"))
			assertDiff("new diff")
			require.NoError(t, repo.UpdateDiffOutput(ctx, "old", "late old diff"))
			assertDiff("new diff")
			require.NoError(t, repo.UpdateDiffOutput(ctx, "new", ""))
			assertDiff("new diff")
			// A higher rowid must not outrank a later started_at.
			_, err = db.Exec(`INSERT INTO executions(id,task_id,started_at) VALUES('backdated','t','2025-01-01')`)
			require.NoError(t, err)
			require.NoError(t, repo.UpdateDiffOutput(ctx, "backdated", "late insert"))
			assertDiff("new diff")
			// Real concurrent calls exercise transaction serialization as well as both
			// deterministic overlap orders above.
			var wg sync.WaitGroup
			errs := make(chan error, 20)
			for i := 0; i < 10; i++ {
				for _, id := range []string{"old", "new"} {
					wg.Add(1)
					go func(id string) { defer wg.Done(); errs <- repo.UpdateDiffOutput(ctx, id, id+" concurrent") }(id)
				}
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}
			assertDiff("new concurrent")
			old, err := repo.GetByID(ctx, "old")
			require.NoError(t, err)
			require.Empty(t, old.DiffOutput)
			require.Equal(t, "old prompt", old.PromptSent)
			require.NoError(t, NewTaskRepo(db, nil).Delete(ctx, "t"))
			require.NoError(t, repo.UpdateDiffOutput(ctx, "new", "after deletion"))
			var n int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM executions WHERE task_id='t'`).Scan(&n))
			require.Zero(t, n)
			got, err := repo.GetLatestNonEmptyDiffOutput(ctx, "other")
			require.NoError(t, err)
			require.Equal(t, "other diff", got)
		})
	}
}

func TestSwarmParentDiffRetention(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewExecutionRepo(db)
	ctx := context.Background()
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title) VALUES('parent','default','Parent')`)
	require.NoError(t, err)
	save := func(merger, diff string) {
		t.Helper()
		require.NoError(t, repo.UpsertSwarmParentResult(ctx, "parent", merger, "output "+merger, diff, 123))
	}
	assertDiff := func(want string) {
		t.Helper()
		got, err := repo.GetLatestNonEmptyDiffOutput(ctx, "parent")
		require.NoError(t, err)
		require.Equal(t, want, got)
		var n int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM executions WHERE task_id='parent' AND diff_output != ''`).Scan(&n))
		require.Equal(t, 1, n)
	}
	save("one", "first")
	save("one", "updated")
	assertDiff("updated")
	save("one", "")
	save("two", "")
	assertDiff("updated")
	save("two", "second")
	save("one", "late")
	save("two", "")
	assertDiff("second")
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM executions WHERE task_id='parent' AND duration_ms=123`).Scan(&n))
	require.Equal(t, 2, n)
	var id string
	require.NoError(t, db.QueryRow(`SELECT id FROM executions WHERE task_id='parent' AND prompt_sent='Swarm merger final result from execution two'`).Scan(&id))
	latest, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "output two", latest.Output)
}
