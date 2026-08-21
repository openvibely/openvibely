package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/openvibely/openvibely/internal/testutil"
)

const taskCapacityCountBenchmarkTasks = 200000

func seedTaskCapacityCountBenchmarkFixture(tb testing.TB, db *sql.DB) {
	tb.Helper()
	tx, err := db.Begin()
	if err != nil {
		tb.Fatalf("begin benchmark fixture transaction: %v", err)
	}
	defer tx.Rollback()

	projectStmt, err := tx.Prepare(`INSERT INTO projects (id, name, description, repo_path) VALUES (?, ?, '', '')`)
	if err != nil {
		tb.Fatalf("prepare projects: %v", err)
	}
	defer projectStmt.Close()
	for i := 0; i < 500; i++ {
		if _, err := projectStmt.Exec(fmt.Sprintf("capacity-project-%03d", i), fmt.Sprintf("Capacity Project %03d", i)); err != nil {
			tb.Fatalf("insert project %d: %v", i, err)
		}
	}

	taskStmt, err := tx.Prepare(`INSERT INTO tasks (id, project_id, title, category, priority, status, prompt, display_order) VALUES (?, ?, ?, ?, 0, ?, 'p', ?)`)
	if err != nil {
		tb.Fatalf("prepare tasks: %v", err)
	}
	defer taskStmt.Close()
	for i := 0; i < taskCapacityCountBenchmarkTasks; i++ {
		projectID := fmt.Sprintf("capacity-project-%03d", i%500)
		category := "active"
		status := "completed"
		if i < 500 {
			status = "pending"
		} else if i < 1000 {
			status = "queued"
		} else if i%5 == 0 {
			category = "backlog"
			status = "pending"
		}
		if _, err := taskStmt.Exec(fmt.Sprintf("capacity-task-%06d", i), projectID, fmt.Sprintf("Capacity Task %06d", i), category, status, i); err != nil {
			tb.Fatalf("insert task %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatalf("commit benchmark fixture: %v", err)
	}
}

func benchmarkCountPendingByProjectQuery(b *testing.B, db *sql.DB, query string) {
	b.Helper()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			b.Fatalf("count pending by project: %v", err)
		}
		var projects, total int
		for rows.Next() {
			var projectID string
			var count int
			if err := rows.Scan(&projectID, &count); err != nil {
				rows.Close()
				b.Fatalf("scan count: %v", err)
			}
			projects++
			total += count
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			b.Fatalf("iterate counts: %v", err)
		}
		rows.Close()
		if projects != 500 || total != 1000 {
			b.Fatalf("count result projects=%d total=%d, want projects=500 total=1000", projects, total)
		}
	}
}

func BenchmarkTaskRepoCountPendingByProjectCapacityIndex(b *testing.B) {
	for _, tc := range []struct {
		name    string
		indexed bool
	}{
		{name: "category_index_baseline", indexed: false},
		{name: "covering_capacity_index", indexed: true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			db := testutil.NewTestDB(b)
			seedTaskCapacityCountBenchmarkFixture(b, db)
			if !tc.indexed {
				if _, err := db.Exec(`DROP INDEX IF EXISTS idx_tasks_active_pending_capacity_counts`); err != nil {
					b.Fatalf("drop capacity index: %v", err)
				}
			}
			benchmarkCountPendingByProjectQuery(b, db, countPendingByProjectSQL)
		})
	}
}
