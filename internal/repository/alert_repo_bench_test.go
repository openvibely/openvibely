package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/database"
	"github.com/openvibely/openvibely/internal/models"
)

// newAlertBenchDB opens a fully migrated in-memory database usable from both
// tests and benchmarks. It applies the same migrations and pragmas as
// production via database.New and closes the database on cleanup.
func newAlertBenchDB(tb testing.TB) *sql.DB {
	tb.Helper()
	db, err := database.New(":memory:")
	if err != nil {
		tb.Fatalf("opening benchmark database: %v", err)
	}
	tb.Cleanup(func() { db.Close() })
	return db
}

// alertBenchFixture describes the production-shaped population used by the
// project-scoped alert ordering benchmarks and query-plan assertions.
const (
	alertBenchTargetProjectID  = "bench-target-project"
	alertBenchTargetRows       = 3000 // older alerts owned by the target project
	alertBenchOtherProjects    = 99   // additional projects
	alertBenchRowsPerOther     = 3000 // newer alerts each other project owns
	alertBenchTotalOtherRows   = alertBenchOtherProjects * alertBenchRowsPerOther
	alertBenchTotalRows        = alertBenchTargetRows + alertBenchTotalOtherRows // 300,000
	alertBenchIndexComposite   = "idx_alerts_project_created"
	alertBenchIndexProjectOnly = "idx_alerts_project_id"
)

// seedAlertBenchFixture populates a fully migrated in-memory database with more
// than 300,000 alerts spread across many projects. The target project's newest
// rows are deliberately older than every other project's rows, so a plan that
// scans global creation order while filtering by project must scan far into the
// table before collecting the newest 100 target rows.
func seedAlertBenchFixture(tb testing.TB, db *sql.DB) {
	tb.Helper()

	// Foreign keys require the referenced projects to exist first. Disable the
	// constraint check for the bulk insert to keep fixture construction fast and
	// deterministic; the seeded rows are internally consistent.
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		tb.Fatalf("disabling foreign keys for fixture: %v", err)
	}
	defer func() {
		if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
			tb.Fatalf("re-enabling foreign keys after fixture: %v", err)
		}
	}()

	tx, err := db.Begin()
	if err != nil {
		tb.Fatalf("begin fixture tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`INSERT INTO projects (id, name, description, repo_path) VALUES (?, 'Bench Target', '', '')`, alertBenchTargetProjectID); err != nil {
		tb.Fatalf("insert target project: %v", err)
	}
	for p := 0; p < alertBenchOtherProjects; p++ {
		if _, err := tx.Exec(`INSERT INTO projects (id, name, description, repo_path) VALUES (?, ?, '', '')`,
			fmt.Sprintf("bench-other-%03d", p), fmt.Sprintf("Bench Other %03d", p)); err != nil {
			tb.Fatalf("insert other project %d: %v", p, err)
		}
	}

	// Target project alerts: created in the 2020 window so they are globally the
	// oldest rows in the table.
	if _, err := tx.Exec(`
		WITH RECURSIVE seq(n) AS (
			SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < ?
		)
		INSERT INTO alerts (project_id, title, created_at)
		SELECT ?, 'target-' || n, datetime('2020-01-01 00:00:00', '+' || n || ' seconds')
		FROM seq`, alertBenchTargetRows, alertBenchTargetProjectID); err != nil {
		tb.Fatalf("insert target alerts: %v", err)
	}

	// Other projects' alerts: created in the 2021 window so every one of them is
	// newer than the target project's newest row.
	for p := 0; p < alertBenchOtherProjects; p++ {
		if _, err := tx.Exec(`
			WITH RECURSIVE seq(n) AS (
				SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < ?
			)
			INSERT INTO alerts (project_id, title, created_at)
			SELECT ?, 'other-' || n, datetime('2021-01-01 00:00:00', '+' || ((? * ?) + n) || ' seconds')
			FROM seq`, alertBenchRowsPerOther, fmt.Sprintf("bench-other-%03d", p), p, alertBenchRowsPerOther); err != nil {
			tb.Fatalf("insert other project %d alerts: %v", p, err)
		}
	}

	if err := tx.Commit(); err != nil {
		tb.Fatalf("commit fixture: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM alerts`).Scan(&count); err != nil {
		tb.Fatalf("counting fixture alerts: %v", err)
	}
	if count != alertBenchTotalRows {
		tb.Fatalf("fixture alert count = %d, want %d", count, alertBenchTotalRows)
	}
}

// setAlertBenchIndexes configures the alerts table to either the pre-migration
// baseline (narrow project-only index, no composite ordering index) or the
// migration-135 candidate (composite project+order index, no redundant
// project-only index). Both share the identical seeded rows.
func setAlertBenchIndexes(tb testing.TB, db *sql.DB, candidate bool) {
	tb.Helper()
	if candidate {
		if _, err := db.Exec(`DROP INDEX IF EXISTS ` + alertBenchIndexProjectOnly); err != nil {
			tb.Fatalf("drop project-only index: %v", err)
		}
		if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS ` + alertBenchIndexComposite +
			` ON alerts(project_id, created_at DESC, id DESC)`); err != nil {
			tb.Fatalf("create composite index: %v", err)
		}
		return
	}
	if _, err := db.Exec(`DROP INDEX IF EXISTS ` + alertBenchIndexComposite); err != nil {
		tb.Fatalf("drop composite index: %v", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS ` + alertBenchIndexProjectOnly + ` ON alerts(project_id)`); err != nil {
		tb.Fatalf("create project-only index: %v", err)
	}
}

func alertBenchExplain(tb testing.TB, db *sql.DB, query string, args ...any) string {
	tb.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		tb.Fatalf("explain query plan: %v", err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			tb.Fatalf("scan explain row: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		tb.Fatalf("explain rows: %v", err)
	}
	return fmt.Sprintf("%v", details)
}

// TestAlertListFilteredNewest100UsesProjectOrderIndex proves that the exact
// production ListFiltered SQL for the unfiltered Alerts page query uses the
// composite project+order index and does not build a temporary B-tree, while
// still returning the correct newest-first, project-scoped page. The query text
// is derived from the same column list and ORDER/LIMIT/OFFSET shape the
// repository emits so plan validation cannot drift from runtime behavior.
func TestAlertListFilteredNewest100UsesProjectOrderIndex(t *testing.T) {
	db := newAlertBenchDB(t)
	seedAlertBenchFixture(t, db)
	repo := NewAlertRepo(db)
	ctx := context.Background()

	productionQuery := `SELECT ` + alertSelectColumns + ` FROM alerts WHERE project_id = ? ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`

	// Baseline (project-only index) builds a temporary sort.
	setAlertBenchIndexes(t, db, false)
	baseline := alertBenchExplain(t, db, productionQuery, alertBenchTargetProjectID, 100, 0)
	if !strings.Contains(baseline, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("baseline plan = %s, want temporary sort", baseline)
	}

	// Candidate (composite project+order index) removes the temporary sort.
	setAlertBenchIndexes(t, db, true)
	candidate := alertBenchExplain(t, db, productionQuery, alertBenchTargetProjectID, 100, 0)
	if !strings.Contains(candidate, alertBenchIndexComposite) {
		t.Fatalf("candidate plan = %s, want composite index", candidate)
	}
	if strings.Contains(candidate, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("candidate plan = %s, want no temporary sort", candidate)
	}

	// Ordering, pagination, and project scoping must be preserved.
	page, err := repo.ListFiltered(ctx, alertBenchTargetProjectID, models.AlertListFilter{Limit: 100, Offset: 0})
	if err != nil {
		t.Fatalf("list filtered: %v", err)
	}
	if len(page) != 100 {
		t.Fatalf("page length = %d, want 100", len(page))
	}
	for i, a := range page {
		if a.ProjectID != alertBenchTargetProjectID {
			t.Fatalf("row %d project = %q, want target project", i, a.ProjectID)
		}
		if i == 0 {
			continue
		}
		prev := page[i-1]
		if a.CreatedAt.After(prev.CreatedAt) ||
			(a.CreatedAt.Equal(prev.CreatedAt) && a.ID > prev.ID) {
			t.Fatalf("row %d out of created_at DESC, id DESC order", i)
		}
	}

	// The second page must not repeat first-page rows.
	next, err := repo.ListFiltered(ctx, alertBenchTargetProjectID, models.AlertListFilter{Limit: 100, Offset: 100})
	if err != nil {
		t.Fatalf("list filtered page 2: %v", err)
	}
	if len(next) != 100 {
		t.Fatalf("page 2 length = %d, want 100", len(next))
	}
	firstIDs := map[string]bool{}
	for _, a := range page {
		firstIDs[a.ID] = true
	}
	for _, a := range next {
		if firstIDs[a.ID] {
			t.Fatalf("page 2 repeated first-page alert %s", a.ID)
		}
	}
}

// BenchmarkAlertListFilteredNewest100 measures the newest-100 project-scoped
// ListFiltered latency, allocations, and per-iteration median for the baseline
// (project-only index) and candidate (composite project+order index) shapes over
// an identical 300,000-row fixture. Run with:
//
//	go test ./internal/repository -run '^$' -bench BenchmarkAlertListFilteredNewest100 -benchmem -count=10
//
// and compare with benchstat. The p50_ms metric is the explicit per-iteration
// median required by the acceptance criteria because ns/op is an aggregate mean.
func BenchmarkAlertListFilteredNewest100(b *testing.B) {
	db := newAlertBenchDB(b)
	seedAlertBenchFixture(b, db)
	repo := NewAlertRepo(db)
	ctx := context.Background()
	filter := models.AlertListFilter{Limit: 100, Offset: 0}

	for _, tc := range []struct {
		name      string
		candidate bool
	}{
		{"baseline", false},
		{"candidate", true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			setAlertBenchIndexes(b, db, tc.candidate)

			// Warm the query and confirm both shapes return the same rows.
			warm, err := repo.ListFiltered(ctx, alertBenchTargetProjectID, filter)
			if err != nil {
				b.Fatalf("warm list: %v", err)
			}
			if len(warm) != 100 {
				b.Fatalf("warm list returned %d rows, want 100", len(warm))
			}

			durations := make([]time.Duration, 0, b.N)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				start := time.Now()
				alerts, err := repo.ListFiltered(ctx, alertBenchTargetProjectID, filter)
				elapsed := time.Since(start)
				if err != nil {
					b.Fatalf("list filtered: %v", err)
				}
				if len(alerts) != 100 {
					b.Fatalf("list returned %d rows, want 100", len(alerts))
				}
				durations = append(durations, elapsed)
			}
			b.StopTimer()

			if len(durations) > 0 {
				sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
				median := durations[len(durations)/2]
				b.ReportMetric(float64(median.Nanoseconds())/1e6, "p50_ms")
			}
		})
	}
}

// BenchmarkAlertInsertThroughput measures single-alert insert latency for the
// baseline (project-only index) and candidate (composite project+order index)
// shapes so the write-throughput tradeoff of the added index is quantified. It
// reports a per-iteration median insert time in addition to ns/op.
func BenchmarkAlertInsertThroughput(b *testing.B) {
	for _, tc := range []struct {
		name      string
		candidate bool
	}{
		{"baseline", false},
		{"candidate", true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			db := newAlertBenchDB(b)
			seedAlertBenchFixture(b, db)
			setAlertBenchIndexes(b, db, tc.candidate)
			repo := NewAlertRepo(db)
			ctx := context.Background()

			durations := make([]time.Duration, 0, b.N)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				a := &models.Alert{
					ProjectID: alertBenchTargetProjectID,
					Type:      models.AlertTaskFailed,
					Severity:  models.SeverityError,
					Title:     fmt.Sprintf("insert-bench-%d", i),
				}
				start := time.Now()
				if err := repo.Create(ctx, a); err != nil {
					b.Fatalf("create alert: %v", err)
				}
				durations = append(durations, time.Since(start))
			}
			b.StopTimer()

			if len(durations) > 0 {
				sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
				median := durations[len(durations)/2]
				b.ReportMetric(float64(median.Nanoseconds())/1e6, "p50_ms")
			}
		})
	}
}

func seedAlertRuntimeProjectionBenchFixture(tb testing.TB, db *sql.DB) {
	tb.Helper()
	tx, err := db.Begin()
	if err != nil {
		tb.Fatalf("begin runtime projection fixture: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO projects (id, name, description, repo_path) VALUES (?, 'Runtime Projection', '', '')`, alertRuntimeProjectionProjectID); err != nil {
		tb.Fatalf("insert runtime projection project: %v", err)
	}
	body := strings.Repeat("body payload ", 3500)              // roughly 42 KiB
	metadataValue := strings.Repeat("metadata payload ", 2300) // roughly 37 KiB once encoded
	metadataJSON, err := json.Marshal(map[string]any{"component": "alerts", "payload": metadataValue})
	if err != nil {
		tb.Fatalf("marshal metadata fixture: %v", err)
	}
	for i := 0; i < alertRuntimeProjectionRows; i++ {
		if _, err := tx.Exec(`INSERT INTO alerts
			(project_id, type, severity, title, message, body, source, metadata_json, decision_state, processing_state, is_read, created_at, updated_at)
			VALUES (?, 'runtime_bench', 'warning', ?, ?, ?, 'benchmark', ?, 'pending', 'unclaimed', ?, datetime('2026-01-01 00:00:00', '+' || ? || ' seconds'), datetime('2026-01-01 00:00:00', '+' || ? || ' seconds'))`,
			alertRuntimeProjectionProjectID, fmt.Sprintf("Runtime alert %03d", i), "Short triage message", body, string(metadataJSON), i%2 == 0, i, i); err != nil {
			tb.Fatalf("insert runtime projection alert %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatalf("commit runtime projection fixture: %v", err)
	}
}

const (
	alertRuntimeProjectionProjectID = "runtime-alert-projection-project"
	alertRuntimeProjectionRows      = 500
	alertRuntimeProjectionLimit     = 50
)

// BenchmarkAlertRuntimeListProjectionResponse measures the production-shaped
// runtime list payload path over alerts with large body and metadata fields. The
// full-row sub-benchmark represents the previous runtime list shape; the compact
// summaries sub-benchmark is the bounded projection used by list_alerts.
func BenchmarkAlertRuntimeListProjectionResponse(b *testing.B) {
	db := newAlertBenchDB(b)
	seedAlertRuntimeProjectionBenchFixture(b, db)
	repo := NewAlertRepo(db)
	ctx := context.Background()
	filter := models.AlertListFilter{Limit: alertRuntimeProjectionLimit, Offset: 0}

	b.Run("full_rows_response", func(b *testing.B) {
		warm, err := repo.ListFiltered(ctx, alertRuntimeProjectionProjectID, filter)
		if err != nil {
			b.Fatalf("warm full list: %v", err)
		}
		payload, err := json.Marshal(map[string]any{"notifications": warm, "project_id": alertRuntimeProjectionProjectID, "offset": 0, "next_offset": alertRuntimeProjectionLimit})
		if err != nil {
			b.Fatalf("marshal warm full payload: %v", err)
		}
		b.ReportMetric(float64(len(payload)), "response_B")
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			alerts, err := repo.ListFiltered(ctx, alertRuntimeProjectionProjectID, filter)
			if err != nil {
				b.Fatalf("list full alerts: %v", err)
			}
			payload, err := json.Marshal(map[string]any{"notifications": alerts, "project_id": alertRuntimeProjectionProjectID, "offset": 0, "next_offset": alertRuntimeProjectionLimit})
			if err != nil {
				b.Fatalf("marshal full payload: %v", err)
			}
			if len(payload) == 0 {
				b.Fatal("empty full payload")
			}
		}
	})

	b.Run("compact_summaries_response", func(b *testing.B) {
		warm, err := repo.ListFilteredSummaries(ctx, alertRuntimeProjectionProjectID, filter)
		if err != nil {
			b.Fatalf("warm summary list: %v", err)
		}
		payload, err := json.Marshal(map[string]any{"notifications": warm, "project_id": alertRuntimeProjectionProjectID, "offset": 0, "next_offset": alertRuntimeProjectionLimit})
		if err != nil {
			b.Fatalf("marshal warm summary payload: %v", err)
		}
		b.ReportMetric(float64(len(payload)), "response_B")
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			summaries, err := repo.ListFilteredSummaries(ctx, alertRuntimeProjectionProjectID, filter)
			if err != nil {
				b.Fatalf("list alert summaries: %v", err)
			}
			payload, err := json.Marshal(map[string]any{"notifications": summaries, "project_id": alertRuntimeProjectionProjectID, "offset": 0, "next_offset": alertRuntimeProjectionLimit})
			if err != nil {
				b.Fatalf("marshal summary payload: %v", err)
			}
			if len(payload) == 0 {
				b.Fatal("empty summary payload")
			}
		}
	})
}
