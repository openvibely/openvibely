package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func BenchmarkThreadInputRepo_ListRecoverableQueuedChatProjectIDsAfter(b *testing.B) {
	b.Run("current", func(b *testing.B) {
		db := newThreadInputBenchmarkDB(b)
		seedThreadInputRecoveryBenchmarkFixture(b, db, 200000, 100)
		repo := NewThreadInputRepo(db)
		ctx := context.Background()

		ids, err := repo.ListRecoverableQueuedChatProjectIDsAfter(ctx, "", 100)
		if err != nil {
			b.Fatalf("warm recoverable chat project page: %v", err)
		}
		if len(ids) != 100 {
			b.Fatalf("warm recoverable chat project count = %d, want 100", len(ids))
		}
		b.Logf("query plan: %s", explainThreadInputQueryPlan(b, db, listRecoverableQueuedChatProjectIDsAfterSQL, "", 100))

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			ids, err := repo.ListRecoverableQueuedChatProjectIDsAfter(ctx, "", 100)
			if err != nil {
				b.Fatalf("ListRecoverableQueuedChatProjectIDsAfter: %v", err)
			}
			if len(ids) != 100 {
				b.Fatalf("recoverable chat project count = %d, want 100", len(ids))
			}
		}
	})
}

func BenchmarkThreadInputRepo_QueuedChatInputWriteIndexOverhead(b *testing.B) {
	b.Run("create_queued", func(b *testing.B) {
		db := newThreadInputBenchmarkDB(b)
		agentID := threadInputBenchmarkAgentID(b, db)
		seedThreadInputWriteProject(b, db)
		repo := NewThreadInputRepo(db)
		ctx := context.Background()

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			input := &models.ThreadInput{
				Scope:         models.ThreadInputScopeChat,
				ProjectID:     "bench-write-project",
				AgentConfigID: agentID,
				InputMode:     models.ThreadInputModeQueued,
				Content:       fmt.Sprintf("queued input %d", i),
				QueuePosition: int64(i + 1),
				ChatMode:      models.ChatModeOrchestrate,
			}
			if err := repo.CreateQueued(ctx, input); err != nil {
				b.Fatalf("CreateQueued: %v", err)
			}
		}
	})

	b.Run("mark_applied", func(b *testing.B) {
		db := newThreadInputBenchmarkDB(b)
		seedThreadInputWriteProject(b, db)
		seedThreadInputPendingWrites(b, db, "apply-bench", b.N)
		repo := NewThreadInputRepo(db)
		ctx := context.Background()

		b.ReportAllocs()
		b.ResetTimer()
		for i := 1; i <= b.N; i++ {
			id := fmt.Sprintf("apply-bench-%08d", i)
			if err := repo.MarkApplied(ctx, id, "", ""); err != nil {
				b.Fatalf("MarkApplied: %v", err)
			}
		}
	})

	b.Run("cancel_pending", func(b *testing.B) {
		db := newThreadInputBenchmarkDB(b)
		seedThreadInputWriteProject(b, db)
		seedThreadInputPendingWrites(b, db, "cancel-bench", b.N)
		repo := NewThreadInputRepo(db)
		ctx := context.Background()

		b.ReportAllocs()
		b.ResetTimer()
		for i := 1; i <= b.N; i++ {
			id := fmt.Sprintf("cancel-bench-%08d", i)
			if _, err := repo.CancelPending(ctx, id); err != nil {
				b.Fatalf("CancelPending: %v", err)
			}
		}
	})
}

func newThreadInputBenchmarkDB(b *testing.B) *sql.DB {
	b.Helper()
	return testutil.NewTestDB(b)
}

func seedThreadInputRecoveryBenchmarkFixture(b *testing.B, db *sql.DB, historicalInputs, pendingProjects int) {
	b.Helper()
	agentID := threadInputBenchmarkAgentID(b, db)
	if _, err := db.Exec(`
		WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 50000)
		INSERT INTO projects (id, name, description, repo_path)
		SELECT 'bench-project-' || printf('%05d', n), 'Bench Project ' || n, '', ''
		FROM seq`); err != nil {
		b.Fatalf("seed benchmark projects: %v", err)
	}
	if _, err := db.Exec(`
		WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < ?)
		INSERT INTO thread_inputs (id, scope, project_id, agent_config_id, input_mode, input_status, content, queue_position, created_at)
		SELECT
			'hist-bench-' || printf('%06d', n),
			'chat',
			'bench-project-' || printf('%05d', ((n - 1) % 50000) + 1),
			?,
			'queued',
			CASE WHEN n % 2 = 0 THEN 'applied' ELSE 'cancelled' END,
			'historical chat input',
			n,
			datetime('2026-01-01 00:00:00', '+' || n || ' seconds')
		FROM seq`, historicalInputs, agentID); err != nil {
		b.Fatalf("seed historical benchmark thread inputs: %v", err)
	}
	if _, err := db.Exec(`
		WITH RECURSIVE seq(n) AS (SELECT 50000 - ? + 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 50000)
		INSERT INTO thread_inputs (id, scope, project_id, agent_config_id, input_mode, input_status, content, queue_position, created_at, chat_mode)
		SELECT
			'pending-bench-' || printf('%05d', n),
			'chat',
			'bench-project-' || printf('%05d', n),
			?,
			'queued',
			'pending',
			'pending chat input',
			n,
			'2026-01-04 00:00:00',
			'orchestrate'
		FROM seq`, pendingProjects, agentID); err != nil {
		b.Fatalf("seed pending benchmark thread inputs: %v", err)
	}
}

func seedThreadInputWriteProject(b *testing.B, db *sql.DB) {
	b.Helper()
	if _, err := db.Exec(`INSERT INTO projects (id, name, description, repo_path) VALUES ('bench-write-project', 'Bench Write Project', '', '')`); err != nil {
		b.Fatalf("seed write benchmark project: %v", err)
	}
}

func seedThreadInputPendingWrites(b *testing.B, db *sql.DB, prefix string, count int) {
	b.Helper()
	if count <= 0 {
		return
	}
	agentID := threadInputBenchmarkAgentID(b, db)
	if _, err := db.Exec(`
		WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < ?)
		INSERT INTO thread_inputs (id, scope, project_id, agent_config_id, input_mode, input_status, content, queue_position, chat_mode)
		SELECT ? || '-' || printf('%08d', n), 'chat', 'bench-write-project', ?, 'queued', 'pending', 'pending write input', n, 'orchestrate'
		FROM seq`, count, prefix, agentID); err != nil {
		b.Fatalf("seed pending write benchmark inputs: %v", err)
	}
}

func threadInputBenchmarkAgentID(tb testing.TB, db *sql.DB) string {
	tb.Helper()
	var agentID string
	if err := db.QueryRow(`SELECT id FROM agent_configs WHERE is_default = 1 LIMIT 1`).Scan(&agentID); err != nil {
		tb.Fatalf("load default benchmark agent: %v", err)
	}
	return agentID
}
