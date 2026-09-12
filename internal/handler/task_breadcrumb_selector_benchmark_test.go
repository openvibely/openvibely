package handler

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/web/templates/components"
)

const breadcrumbSelectorBaselineQuery = `SELECT id, title
	FROM tasks
	WHERE project_id = ? AND category != 'chat'
		AND (? = FALSE OR EXISTS (
			SELECT 1 FROM schedules WHERE schedules.task_id = tasks.id
		))
		AND (? = '' OR INSTR(LOWER(title), ?) > 0)
	ORDER BY CASE WHEN id = ? THEN 0 ELSE 1 END,
		CASE WHEN LOWER(title) = ? THEN 0 WHEN LOWER(title) LIKE ? || '%' THEN 1 ELSE 2 END,
		updated_at DESC, id ASC
	LIMIT ?`

const breadcrumbSelectorCandidateQuery = breadcrumbSelectorBaselineQuery

const breadcrumbSelectorCurrentQuery = `SELECT id, title
		FROM tasks
		WHERE project_id = ? AND id = ? AND category != 'chat'
			AND (? = FALSE OR EXISTS (
				SELECT 1 FROM schedules WHERE schedules.task_id = tasks.id
			))`

const breadcrumbSelectorRecencyQuery = `SELECT id, title
		FROM tasks
		WHERE project_id = ? AND category != 'chat'
			AND (? = FALSE OR EXISTS (
				SELECT 1 FROM schedules WHERE schedules.task_id = tasks.id
			))
			AND id != ?
		ORDER BY updated_at DESC, id ASC
		LIMIT ?`

type breadcrumbSelectorBenchmarkCase struct {
	name         string
	search       string
	currentID    string
	scheduleOnly bool
}

func BenchmarkTaskRepoListBreadcrumbSelector(b *testing.B) {
	for _, taskCount := range []int{1000, 10000, 100000, 500000} {
		taskCount := taskCount
		b.Run(fmt.Sprintf("%d_tasks", taskCount), func(b *testing.B) {
			db, repo := newBreadcrumbSelectorBenchmarkRepo(b, taskCount)
			defer db.Close()

			cases := []breadcrumbSelectorBenchmarkCase{
				{name: "EmptyCurrent", currentID: breadcrumbSelectorBenchmarkTaskID(1)},
				{name: "CommonSubstring", search: "deploy", currentID: breadcrumbSelectorBenchmarkTaskID(1)},
				{name: "Unmatched", search: "no-such-breadcrumb-title", currentID: breadcrumbSelectorBenchmarkTaskID(1)},
				{name: "ScheduleOrigin", currentID: breadcrumbSelectorBenchmarkTaskID(10), scheduleOnly: true},
			}
			for _, benchmarkCase := range cases {
				logBreadcrumbSelectorPlanMatrix(b, db, benchmarkCase)
			}
			for _, benchmarkCase := range cases {
				benchmarkCase := benchmarkCase
				b.Run(benchmarkCase.name, func(b *testing.B) {
					b.Run("Before", func(b *testing.B) {
						benchmarkBreadcrumbSelector(b, db, repo, benchmarkCase, false)
					})
					b.Run("After", func(b *testing.B) {
						benchmarkBreadcrumbSelector(b, db, repo, benchmarkCase, true)
					})
				})
			}
		})
	}
}

func benchmarkBreadcrumbSelector(b *testing.B, db *sql.DB, repo *repository.TaskRepo, benchmarkCase breadcrumbSelectorBenchmarkCase, candidate bool) {
	b.Helper()
	ctx := context.Background()
	const limit = 21

	before, err := listBreadcrumbSelectorBaseline(ctx, db, benchmarkCase, limit)
	if err != nil {
		b.Fatalf("baseline breadcrumb selector: %v", err)
	}
	var warm []models.BreadcrumbSelectorItem
	if candidate {
		warm, err = repo.ListBreadcrumbSelector(ctx, "default", benchmarkCase.search, benchmarkCase.currentID, benchmarkCase.scheduleOnly, limit)
	} else {
		warm = before
	}
	if err != nil {
		b.Fatalf("warm breadcrumb selector: %v", err)
	}
	if !equalBreadcrumbSelectorItems(warm, before) {
		b.Fatalf("warm result differs from baseline: before=%v after=%v", before, warm)
	}
	beforeHTMLBytes, err := breadcrumbSelectorHTMLBytes(before, benchmarkCase.currentID)
	if err != nil {
		b.Fatalf("render baseline breadcrumb selector HTML: %v", err)
	}
	warmHTMLBytes, err := breadcrumbSelectorHTMLBytes(warm, benchmarkCase.currentID)
	if err != nil {
		b.Fatalf("render candidate breadcrumb selector HTML: %v", err)
	}
	if warmHTMLBytes != beforeHTMLBytes {
		b.Fatalf("warm HTML bytes = %d, want baseline %d", warmHTMLBytes, beforeHTMLBytes)
	}

	durations := make([]time.Duration, b.N)
	resultBytes := 0
	var lastItems []models.BreadcrumbSelectorItem
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		if candidate {
			lastItems, err = repo.ListBreadcrumbSelector(ctx, "default", benchmarkCase.search, benchmarkCase.currentID, benchmarkCase.scheduleOnly, limit)
		} else {
			lastItems, err = listBreadcrumbSelectorBaseline(ctx, db, benchmarkCase, limit)
		}
		durations[i] = time.Since(start)
		if err != nil {
			b.Fatalf("breadcrumb selector: %v", err)
		}
		if !equalBreadcrumbSelectorItems(lastItems, before) {
			b.Fatalf("result differs from baseline: got=%v want=%v", lastItems, before)
		}
		resultBytes = breadcrumbSelectorResultBytes(lastItems)
	}
	b.StopTimer()
	htmlBytes, err := breadcrumbSelectorHTMLBytes(lastItems, benchmarkCase.currentID)
	if err != nil {
		b.Fatalf("render breadcrumb selector HTML: %v", err)
	}
	if htmlBytes != beforeHTMLBytes {
		b.Fatalf("result HTML bytes = %d, want baseline %d", htmlBytes, beforeHTMLBytes)
	}
	if len(durations) > 0 {
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		b.ReportMetric(float64(durations[len(durations)/2].Nanoseconds())/1e6, "p50_ms")
	}
	b.ReportMetric(float64(resultBytes), "result_bytes/op")
	b.ReportMetric(float64(htmlBytes), "html_bytes/op")
}

func listBreadcrumbSelectorBaseline(ctx context.Context, db *sql.DB, benchmarkCase breadcrumbSelectorBenchmarkCase, limit int) ([]models.BreadcrumbSelectorItem, error) {
	rows, err := db.QueryContext(ctx, breadcrumbSelectorBaselineQuery,
		"default", benchmarkCase.scheduleOnly, strings.ToLower(benchmarkCase.search), strings.ToLower(benchmarkCase.search),
		benchmarkCase.currentID, strings.ToLower(benchmarkCase.search), strings.ToLower(benchmarkCase.search), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]models.BreadcrumbSelectorItem, 0, limit)
	for rows.Next() {
		var item models.BreadcrumbSelectorItem
		if err := rows.Scan(&item.ID, &item.Name); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func equalBreadcrumbSelectorItems(left, right []models.BreadcrumbSelectorItem) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].ID != right[i].ID || left[i].Name != right[i].Name {
			return false
		}
	}
	return true
}

func breadcrumbSelectorResultBytes(items []models.BreadcrumbSelectorItem) int {
	resultBytes := 0
	for _, item := range items {
		resultBytes += len(item.ID) + len(item.Name)
	}
	return resultBytes
}

func breadcrumbSelectorHTMLBytes(items []models.BreadcrumbSelectorItem, currentID string) (int, error) {
	page := append([]models.BreadcrumbSelectorItem(nil), items...)
	hasMore := len(page) > 20
	if hasMore {
		page = page[:20]
	}
	for i := range page {
		page[i].URL = "/tasks/" + page[i].ID + "?project_id=default"
	}
	var output bytes.Buffer
	if err := components.BreadcrumbSelectorResults("Task", currentID, page, hasMore).Render(context.Background(), &output); err != nil {
		return 0, err
	}
	return output.Len(), nil
}

func TestTaskRepo_BreadcrumbSelectorQueryPlans(t *testing.T) {
	db, _ := newBreadcrumbSelectorBenchmarkRepo(t, 1000)
	defer db.Close()

	cases := []breadcrumbSelectorBenchmarkCase{
		{name: "EmptyCurrent", currentID: breadcrumbSelectorBenchmarkTaskID(1)},
		{name: "CommonSubstring", search: "deploy", currentID: breadcrumbSelectorBenchmarkTaskID(1)},
		{name: "Unmatched", search: "no-such-breadcrumb-title", currentID: breadcrumbSelectorBenchmarkTaskID(1)},
		{name: "ScheduleOrigin", currentID: breadcrumbSelectorBenchmarkTaskID(10), scheduleOnly: true},
	}
	for _, benchmarkCase := range cases {
		t.Run(benchmarkCase.name, func(t *testing.T) {
			baselinePlan := breadcrumbSelectorExplain(t, db, breadcrumbSelectorBaselineQuery, breadcrumbSelectorBaselineArgs(benchmarkCase, 21)...)
			if !strings.Contains(baselinePlan, "USE TEMP B-TREE FOR ORDER BY") {
				t.Fatalf("baseline plan = %s, want temporary relevance sort", baselinePlan)
			}
			if benchmarkCase.search != "" {
				candidatePlan := breadcrumbSelectorExplain(t, db, breadcrumbSelectorCandidateQuery, breadcrumbSelectorBaselineArgs(benchmarkCase, 21)...)
				t.Logf("baseline plan: %s; candidate relevance plan: %s", baselinePlan, candidatePlan)
				return
			}

			currentPlan := breadcrumbSelectorExplain(t, db, breadcrumbSelectorCurrentQuery, "default", benchmarkCase.currentID, benchmarkCase.scheduleOnly)
			recencyPlan := breadcrumbSelectorExplain(t, db, breadcrumbSelectorRecencyQuery, "default", benchmarkCase.scheduleOnly, benchmarkCase.currentID, 20)
			if !strings.Contains(currentPlan, "SEARCH tasks") || strings.Contains(currentPlan, "SCAN tasks") {
				t.Fatalf("candidate current-task plan = %s, want an indexed task lookup", currentPlan)
			}
			if !strings.Contains(recencyPlan, "idx_tasks_discovery_order") || strings.Contains(recencyPlan, "USE TEMP B-TREE FOR ORDER BY") {
				t.Fatalf("candidate recency plan = %s, want discovery-order index without temporary sort", recencyPlan)
			}
			t.Logf("baseline plan: %s; candidate current-task plan: %s; candidate recency plan: %s", baselinePlan, currentPlan, recencyPlan)
		})
	}
}

func breadcrumbSelectorBaselineArgs(benchmarkCase breadcrumbSelectorBenchmarkCase, limit int) []any {
	search := strings.ToLower(benchmarkCase.search)
	return []any{"default", benchmarkCase.scheduleOnly, search, search, benchmarkCase.currentID, search, search, limit}
}

func logBreadcrumbSelectorPlanMatrix(tb testing.TB, db *sql.DB, benchmarkCase breadcrumbSelectorBenchmarkCase) {
	tb.Helper()
	baselinePlan := breadcrumbSelectorExplain(tb, db, breadcrumbSelectorBaselineQuery, breadcrumbSelectorBaselineArgs(benchmarkCase, 21)...)
	if benchmarkCase.search != "" {
		candidatePlan := breadcrumbSelectorExplain(tb, db, breadcrumbSelectorCandidateQuery, breadcrumbSelectorBaselineArgs(benchmarkCase, 21)...)
		tb.Logf("%s plans: baseline=%s; candidate relevance=%s", benchmarkCase.name, baselinePlan, candidatePlan)
		return
	}
	currentPlan := breadcrumbSelectorExplain(tb, db, breadcrumbSelectorCurrentQuery, "default", benchmarkCase.currentID, benchmarkCase.scheduleOnly)
	recencyPlan := breadcrumbSelectorExplain(tb, db, breadcrumbSelectorRecencyQuery, "default", benchmarkCase.scheduleOnly, benchmarkCase.currentID, 20)
	if !strings.Contains(recencyPlan, "idx_tasks_discovery_order") || strings.Contains(recencyPlan, "USE TEMP B-TREE FOR ORDER BY") {
		tb.Fatalf("%s candidate recency plan = %s, want discovery-order index without temporary sort", benchmarkCase.name, recencyPlan)
	}
	tb.Logf("%s plans: baseline=%s; candidate current-task=%s; candidate recency=%s", benchmarkCase.name, baselinePlan, currentPlan, recencyPlan)
}

func TestTaskRepo_BreadcrumbSelectorLatencyAcceptanceGates(t *testing.T) {
	for _, taskCount := range []int{10000, 100000, 500000} {
		taskCount := taskCount
		t.Run(fmt.Sprintf("%d_tasks_empty_current", taskCount), func(t *testing.T) {
			db, repo := newBreadcrumbSelectorBenchmarkRepoWithSchedules(t, taskCount, false)
			defer db.Close()
			benchmarkCase := breadcrumbSelectorBenchmarkCase{name: "EmptyCurrent", currentID: breadcrumbSelectorBenchmarkTaskID(1)}
			before, err := listBreadcrumbSelectorBaseline(context.Background(), db, benchmarkCase, 21)
			if err != nil {
				t.Fatalf("baseline warm-up: %v", err)
			}
			after, err := repo.ListBreadcrumbSelector(context.Background(), "default", benchmarkCase.search, benchmarkCase.currentID, benchmarkCase.scheduleOnly, 21)
			if err != nil {
				t.Fatalf("candidate warm-up: %v", err)
			}
			if !equalBreadcrumbSelectorItems(before, after) {
				t.Fatalf("warm-up result differs: before=%v after=%v", before, after)
			}

			beforeMedian := measureBreadcrumbSelectorMedian(t, db, repo, benchmarkCase, false)
			afterMedian := measureBreadcrumbSelectorMedian(t, db, repo, benchmarkCase, true)
			t.Logf("empty/current latency gate: before_p50=%s after_p50=%s improvement=%.2f%%", beforeMedian, afterMedian, 100*(1-float64(afterMedian)/float64(beforeMedian)))
			if taskCount >= 100000 && afterMedian*10 > beforeMedian*3 {
				t.Fatalf("candidate median %s is not at least 70%% faster than baseline %s", afterMedian, beforeMedian)
			}
			if taskCount == 10000 && afterMedian*10 > beforeMedian*11 {
				t.Fatalf("candidate median %s is more than 10%% slower than baseline %s", afterMedian, beforeMedian)
			}
		})
	}
}

func measureBreadcrumbSelectorMedian(tb testing.TB, db *sql.DB, repo *repository.TaskRepo, benchmarkCase breadcrumbSelectorBenchmarkCase, candidate bool) time.Duration {
	tb.Helper()
	const (
		samples        = 5
		callsPerSample = 3
		limit          = 21
	)
	durations := make([]time.Duration, samples)
	ctx := context.Background()
	baseline, err := listBreadcrumbSelectorBaseline(ctx, db, benchmarkCase, limit)
	if err != nil {
		tb.Fatalf("baseline measurement warm-up: %v", err)
	}
	for sample := 0; sample < samples; sample++ {
		start := time.Now()
		for call := 0; call < callsPerSample; call++ {
			var items []models.BreadcrumbSelectorItem
			if candidate {
				items, err = repo.ListBreadcrumbSelector(ctx, "default", benchmarkCase.search, benchmarkCase.currentID, benchmarkCase.scheduleOnly, limit)
			} else {
				items, err = listBreadcrumbSelectorBaseline(ctx, db, benchmarkCase, limit)
			}
			if err != nil {
				tb.Fatalf("breadcrumb selector sample: %v", err)
			}
			if !equalBreadcrumbSelectorItems(items, baseline) {
				tb.Fatalf("measurement result differs from baseline: got=%v want=%v", items, baseline)
			}
		}
		durations[sample] = time.Since(start) / callsPerSample
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	return durations[len(durations)/2]
}

func TestTaskRepo_BreadcrumbSelectorWriteStorageScope(t *testing.T) {
	db := testutil.NewTestDB(t)
	defer db.Close()
	repo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	fingerprint := func() string {
		var schema string
		err := db.QueryRowContext(ctx, `SELECT COALESCE(group_concat(name || ':' || COALESCE(sql, ''), '|'), '')
			FROM (SELECT name, sql FROM sqlite_master WHERE type = 'index' ORDER BY name)`).Scan(&schema)
		if err != nil {
			t.Fatalf("read schema fingerprint: %v", err)
		}
		return schema
	}
	beforeSchema := fingerprint()
	beforePages := breadcrumbSelectorSQLitePageCount(t, db)

	task := &models.Task{ProjectID: "default", Title: "breadcrumb write scope", Category: models.CategoryBacklog, Priority: 2, Status: models.StatusPending}
	if err := repo.Create(ctx, task); err != nil {
		t.Fatalf("create write-scope task: %v", err)
	}
	task.Title = "breadcrumb write scope updated"
	if err := repo.Update(ctx, task); err != nil {
		t.Fatalf("update write-scope task: %v", err)
	}
	if err := repo.Delete(ctx, task.ID); err != nil {
		t.Fatalf("delete write-scope task: %v", err)
	}

	afterSchema := fingerprint()
	afterPages := breadcrumbSelectorSQLitePageCount(t, db)
	if afterSchema != beforeSchema {
		t.Fatalf("schema/index fingerprint changed across task writes: before=%s after=%s", beforeSchema, afterSchema)
	}
	t.Logf("no schema/index/search-column change in this implementation; existing discovery-index write scope pages before=%d after=%d", beforePages, afterPages)
}

func BenchmarkTaskRepoBreadcrumbSelectorWriteMaintenance(b *testing.B) {
	db, repo := newBreadcrumbSelectorBenchmarkRepo(b, 1000)
	defer db.Close()
	ctx := context.Background()
	beforePages := breadcrumbSelectorSQLitePageCount(b, db)
	beforeFreePages := breadcrumbSelectorSQLiteFreelistCount(b, db)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		task := &models.Task{ProjectID: "default", Title: fmt.Sprintf("breadcrumb-write-benchmark-%d", i), Category: models.CategoryBacklog, Priority: 2, Status: models.StatusPending}
		if err := repo.Create(ctx, task); err != nil {
			b.Fatalf("create task: %v", err)
		}
		task.Title += " updated"
		if err := repo.Update(ctx, task); err != nil {
			b.Fatalf("update task: %v", err)
		}
		if err := repo.Delete(ctx, task.ID); err != nil {
			b.Fatalf("delete task: %v", err)
		}
	}
	b.StopTimer()
	afterPages := breadcrumbSelectorSQLitePageCount(b, db)
	afterFreePages := breadcrumbSelectorSQLiteFreelistCount(b, db)
	pageDelta := afterPages - beforePages
	freePageDelta := afterFreePages - beforeFreePages
	if b.N > 0 {
		b.ReportMetric(float64(pageDelta)/float64(b.N), "storage_pages/op")
		b.ReportMetric(float64(freePageDelta)/float64(b.N), "freelist_pages/op")
	}
	b.Logf("existing discovery index write/update/delete maintenance: page_size=%d page_delta=%d freelist_delta=%d; no new schema/index/search column is introduced", breadcrumbSelectorSQLitePageSize(b, db), pageDelta, freePageDelta)
}

func breadcrumbSelectorSQLitePageCount(tb testing.TB, db *sql.DB) int64 {
	tb.Helper()
	var pages int64
	if err := db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		tb.Fatalf("read SQLite page count: %v", err)
	}
	return pages
}

func breadcrumbSelectorSQLiteFreelistCount(tb testing.TB, db *sql.DB) int64 {
	tb.Helper()
	var pages int64
	if err := db.QueryRow("PRAGMA freelist_count").Scan(&pages); err != nil {
		tb.Fatalf("read SQLite freelist count: %v", err)
	}
	return pages
}

func breadcrumbSelectorSQLitePageSize(tb testing.TB, db *sql.DB) int64 {
	tb.Helper()
	var size int64
	if err := db.QueryRow("PRAGMA page_size").Scan(&size); err != nil {
		tb.Fatalf("read SQLite page size: %v", err)
	}
	return size
}

func breadcrumbSelectorExplain(tb testing.TB, db *sql.DB, query string, args ...any) string {
	tb.Helper()
	rows, err := db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		tb.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			tb.Fatalf("scan query plan: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		tb.Fatalf("iterate query plan: %v", err)
	}
	return strings.Join(details, "\n")
}

func newBreadcrumbSelectorBenchmarkRepo(tb testing.TB, taskCount int) (*sql.DB, *repository.TaskRepo) {
	return newBreadcrumbSelectorBenchmarkRepoWithSchedules(tb, taskCount, true)
}

func newBreadcrumbSelectorBenchmarkRepoWithSchedules(tb testing.TB, taskCount int, includeSchedules bool) (*sql.DB, *repository.TaskRepo) {
	tb.Helper()
	db := testutil.NewTestDB(tb)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		tb.Fatalf("begin breadcrumb selector fixture: %v", err)
	}
	_, err = tx.ExecContext(context.Background(), `
		WITH RECURSIVE seq(n) AS (
			SELECT 1
			UNION ALL
			SELECT n + 1 FROM seq WHERE n < ?
		)
		INSERT INTO tasks (id, project_id, title, category, priority, status, prompt, chain_config, swarm_config, updated_at)
		SELECT
			'breadcrumb-benchmark-' || printf('%06d', n),
			'default',
			CASE
				WHEN n % 100 = 0 THEN 'deploy exact ' || printf('%06d', n)
				WHEN n % 100 < 5 THEN 'deploy service ' || printf('%06d', n)
				WHEN n % 100 < 10 THEN 'release deploy ' || printf('%06d', n)
				ELSE 'Breadcrumb task ' || printf('%06d', n)
			END,
			CASE WHEN n % 20 = 0 THEN 'chat' ELSE 'active' END,
			(n % 4) + 1,
			'pending', 'p', '{}', '{}',
			datetime('2026-01-01 00:00:00', '+' || n || ' seconds')
		FROM seq`, taskCount)
	if err != nil {
		_ = tx.Rollback()
		tb.Fatalf("seed %d breadcrumb selector tasks: %v", taskCount, err)
	}
	if !includeSchedules {
		if err := tx.Commit(); err != nil {
			tb.Fatalf("commit %d-task breadcrumb selector fixture: %v", taskCount, err)
		}
		return db, repository.NewTaskRepo(db, nil)
	}
	_, err = tx.ExecContext(context.Background(), `
		INSERT INTO schedules (id, task_id, run_at, repeat_type, repeat_interval, enabled, next_run)
		SELECT
			'breadcrumb-schedule-' || printf('%06d', n),
			'breadcrumb-benchmark-' || printf('%06d', n),
			datetime('2026-01-01 00:00:00', '+' || n || ' seconds'),
			'daily', 1, 1,
			datetime('2026-01-01 00:00:00', '+' || n || ' seconds')
		FROM (
			WITH RECURSIVE seq(n) AS (
				SELECT 1
				UNION ALL
				SELECT n + 1 FROM seq WHERE n < ?
			)
			SELECT n FROM seq WHERE n % 10 = 0 AND n % 20 != 0
		)`, taskCount)
	if err != nil {
		_ = tx.Rollback()
		tb.Fatalf("seed breadcrumb selector schedules: %v", err)
	}
	if err := tx.Commit(); err != nil {
		tb.Fatalf("commit breadcrumb selector fixture: %v", err)
	}
	return db, repository.NewTaskRepo(db, nil)
}

func breadcrumbSelectorBenchmarkTaskID(n int) string {
	return fmt.Sprintf("breadcrumb-benchmark-%06d", n)
}
