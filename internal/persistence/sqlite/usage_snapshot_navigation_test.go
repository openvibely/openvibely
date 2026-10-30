package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence/sqlite/database"
)

func TestAccountSnapshotHistoryDoesNotStallTaskRead(t *testing.T) {
	connections, err := database.NewReadWrite(filepath.Join(t.TempDir(), "usage-navigation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connections.Close()
	if got := connections.Reader.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("regression requires the production single reader, got %d", got)
	}
	_, err = connections.Writer.Exec(`
		INSERT INTO projects(id, name) VALUES ('usage-project', 'Usage project');
		INSERT INTO tasks(id, project_id, title, category, status) VALUES ('navigation-task', 'usage-project', 'Navigation task', 'active', 'pending');
		INSERT INTO oauth_connections(id, provider) VALUES ('usage-0', 'openai'), ('usage-1', 'openai'), ('usage-2', 'anthropic');
		WITH RECURSIVE history(n) AS (
			VALUES(0) UNION ALL SELECT n+1 FROM history WHERE n < 8999
		)
		INSERT INTO account_usage_snapshots(id, provider, oauth_connection_id, fetched_at, created_at)
		SELECT 'history-' || n, CASE WHEN n % 3 = 2 THEN 'anthropic' ELSE 'openai' END,
			'usage-' || (n % 3), datetime('2026-01-01', '+' || n || ' seconds'), '2026-01-01'
		FROM history;
	`)
	if err != nil {
		t.Fatal(err)
	}

	// Exercise the actual card snapshot query, rather than artificially holding
	// pool connections. Historical snapshots must not monopolize the sole reader.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan struct{})
	var snapshots []models.AccountUsageSnapshot
	var snapshotErr error
	go func() {
		snapshots, snapshotErr = NewUsageRepo(connections.Reader).GetLatestAccountUsageSnapshots(ctx, "")
		close(done)
	}()
	defer func() { cancel(); <-done }()
waitForQuery:
	for connections.Reader.Stats().InUse == 0 {
		select {
		case <-done:
			break waitForQuery
		case <-ctx.Done():
			t.Fatal("snapshot query did not start")
		case <-time.After(time.Millisecond):
		}
	}
	navigationCtx, cancelNavigation := context.WithTimeout(context.Background(), time.Second)
	defer cancelNavigation()
	task, err := NewTaskRepo(connections.Reader, nil).GetByID(navigationCtx, "navigation-task")
	if err != nil {
		t.Fatalf("task navigation stalled behind account snapshot history: %v", err)
	}
	if task == nil || task.ID != "navigation-task" {
		t.Fatalf("unexpected task: %+v", task)
	}
	<-done
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
	want := map[string]string{"usage-0": "history-8997", "usage-1": "history-8998", "usage-2": "history-8999"}
	if len(snapshots) != len(want) {
		t.Fatalf("got %d snapshots, want %d", len(snapshots), len(want))
	}
	for _, snapshot := range snapshots {
		if snapshot.ID != want[snapshot.OAuthConnectionID] {
			t.Errorf("connection %s: snapshot %s, want %s", snapshot.OAuthConnectionID, snapshot.ID, want[snapshot.OAuthConnectionID])
		}
	}
}

func TestLatestAccountSnapshotsPreserveLegacyAndConnectionGroups(t *testing.T) {
	connections, err := database.NewReadWrite(filepath.Join(t.TempDir(), "usage-groups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connections.Close()
	_, err = connections.Writer.Exec(`
		INSERT INTO agent_configs(id, name, provider, model) VALUES ('model-a','A','openai','a'), ('model-b','B','openai','b');
		INSERT INTO oauth_connections(id, provider) VALUES ('connection', 'openai');
		INSERT INTO account_usage_snapshots(id, provider, account_id, agent_config_id, oauth_connection_id, fetched_at, created_at) VALUES
		('legacy-null', 'openai', NULL, 'model-a', NULL, '2026-01-01', '2026-01-01 00:00:00'),
		('legacy-empty', 'openai', '', 'model-a', NULL, '2026-01-01', '2026-01-01 00:00:01'),
		('legacy-other-model', 'openai', '', 'model-b', NULL, '2026-01-01', '2026-01-01'),
		('legacy-other-provider', 'anthropic', '', 'model-a', NULL, '2026-01-01', '2026-01-01'),
		('linked-old', 'openai', 'old-account', 'model-a', 'connection', '2026-01-01', '2026-01-01'),
		('linked-new', 'openai', 'new-account', 'model-b', 'connection', '2026-01-02', '2026-01-02');
	`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		provider string
		ids      []string
	}{
		{"", []string{"legacy-empty", "legacy-other-model", "legacy-other-provider", "linked-new"}},
		{"openai", []string{"legacy-empty", "legacy-other-model", "linked-new"}},
		{"anthropic", []string{"legacy-other-provider"}},
		{"missing", nil},
	} {
		t.Run("provider="+tc.provider, func(t *testing.T) {
			snapshots, err := NewUsageRepo(connections.Reader).GetLatestAccountUsageSnapshots(context.Background(), tc.provider)
			if err != nil {
				t.Fatal(err)
			}
			want := make(map[string]bool)
			for _, id := range tc.ids {
				want[id] = true
			}
			if len(snapshots) != len(want) {
				t.Fatalf("got %d snapshots, want %d", len(snapshots), len(want))
			}
			for _, snapshot := range snapshots {
				if !want[snapshot.ID] {
					t.Errorf("unexpected snapshot %q", snapshot.ID)
				}
				delete(want, snapshot.ID)
			}
			if len(want) != 0 {
				t.Errorf("missing snapshots: %v", want)
			}
		})
	}
}
