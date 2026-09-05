package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/testutil"
)

const automationStalePRDiscoveryProjectID = "automation-stale-pr-project"

func TestAutomationRepoListAutomationsWithStaleExternalPullRequestsUsesStalePullRequestIndex(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedAutomationStalePRDiscoveryFixture(t, db, 10, 200)
	repo := NewAutomationRepo(db)
	ctx := context.Background()
	staleBefore := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)

	noStalePlan := explainAutomationStalePRDiscoveryPlan(t, db)
	assertAutomationStalePRDiscoveryPlan(t, noStalePlan)
	rows, err := repo.ListAutomationsWithStaleExternalPullRequests(ctx, staleBefore, 100)
	if err != nil {
		t.Fatalf("ListAutomationsWithStaleExternalPullRequests no stale: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("no-stale rows = %v, want empty", rows)
	}

	markSparseAutomationStalePRs(t, db)
	sparsePlan := explainAutomationStalePRDiscoveryPlan(t, db)
	assertAutomationStalePRDiscoveryPlan(t, sparsePlan)
	rows, err = repo.ListAutomationsWithStaleExternalPullRequests(ctx, staleBefore, 100)
	if err != nil {
		t.Fatalf("ListAutomationsWithStaleExternalPullRequests sparse stale: %v", err)
	}
	want := [][2]string{
		{automationStalePRDiscoveryProjectID, "automation-stale-auto-003"},
		{automationStalePRDiscoveryProjectID, "automation-stale-auto-007"},
	}
	if fmt.Sprint(rows) != fmt.Sprint(want) {
		t.Fatalf("sparse stale rows = %v, want %v", rows, want)
	}

	limited, err := repo.ListAutomationsWithStaleExternalPullRequests(ctx, staleBefore, 1)
	if err != nil {
		t.Fatalf("ListAutomationsWithStaleExternalPullRequests limited: %v", err)
	}
	if fmt.Sprint(limited) != fmt.Sprint(want[:1]) {
		t.Fatalf("limited rows = %v, want %v", limited, want[:1])
	}
}

func BenchmarkAutomationStaleExternalPullRequestDiscovery50k(b *testing.B) {
	db := testutil.NewTestDB(b)
	seedAutomationStalePRDiscoveryFixture(b, db, 50, 1000)
	repo := NewAutomationRepo(db)
	ctx := context.Background()
	staleBefore := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)

	optimizedPlan := explainAutomationStalePRDiscoveryPlanForQuery(b, db, listAutomationsWithStaleExternalPullRequestsSQL)
	assertAutomationStalePRDiscoveryPlan(b, optimizedPlan)

	b.Run("no_stale_50k_tracked", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			rows, err := repo.ListAutomationsWithStaleExternalPullRequests(ctx, staleBefore, 100)
			if err != nil {
				b.Fatal(err)
			}
			if len(rows) != 0 {
				b.Fatalf("rows = %d, want 0", len(rows))
			}
		}
	})

	markBenchmarkSparseAutomationStalePRs(b, db, 50)
	b.Run("sparse_stale_50_of_50k_tracked", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			rows, err := repo.ListAutomationsWithStaleExternalPullRequests(ctx, staleBefore, 100)
			if err != nil {
				b.Fatal(err)
			}
			if len(rows) != 50 {
				b.Fatalf("rows = %d, want 50", len(rows))
			}
		}
	})
}

func seedAutomationStalePRDiscoveryFixture(tb testing.TB, db *sql.DB, automationCount, prsPerAutomation int) {
	tb.Helper()
	if automationCount <= 0 || prsPerAutomation <= 0 {
		tb.Fatalf("automationCount=%d prsPerAutomation=%d, want positive", automationCount, prsPerAutomation)
	}
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO projects (id, name, description, repo_path) VALUES (?, 'Automation stale PRs', '', '')`, automationStalePRDiscoveryProjectID); err != nil {
		tb.Fatalf("insert project: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		WITH RECURSIVE seq(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM seq WHERE n < ? - 1)
		INSERT INTO automations (id, project_id, stable_key, name, automation_type, lifecycle_state, published_version_id)
		SELECT
			'automation-stale-auto-' || printf('%03d', n),
			?,
			'automation-stale/' || printf('%03d', n),
			'Automation stale ' || printf('%03d', n),
			'github_sdlc',
			'active',
			'automation-stale-version-' || printf('%03d', n)
		FROM seq`, automationCount, automationStalePRDiscoveryProjectID); err != nil {
		tb.Fatalf("insert automations: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		WITH RECURSIVE seq(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM seq WHERE n < ? - 1)
		INSERT INTO automation_versions (id, project_id, automation_id, version, state, source, adapter_key, published_at)
		SELECT
			'automation-stale-version-' || printf('%03d', n),
			?,
			'automation-stale-auto-' || printf('%03d', n),
			1,
			'published',
			'template',
			'github_sdlc',
			datetime('2024-01-01')
		FROM seq`, automationCount, automationStalePRDiscoveryProjectID); err != nil {
		tb.Fatalf("insert automation versions: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		WITH RECURSIVE seq(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM seq WHERE n < ? - 1)
		INSERT INTO automation_nodes (id, project_id, automation_id, version_id, node_key, name, node_type, role)
		SELECT
			'automation-stale-node-' || printf('%03d', n),
			?,
			'automation-stale-auto-' || printf('%03d', n),
			'automation-stale-version-' || printf('%03d', n),
			'open_pr',
			'Open pull request',
			'action',
			'open_pull_request'
		FROM seq`, automationCount, automationStalePRDiscoveryProjectID); err != nil {
		tb.Fatalf("insert automation nodes: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		WITH RECURSIVE seq(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM seq WHERE n < ? - 1)
		INSERT INTO automation_invocations (id, project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key, status, started_at, completed_at)
		SELECT
			'automation-stale-invocation-' || printf('%03d', n),
			?,
			'automation-stale-auto-' || printf('%03d', n),
			'automation-stale-version-' || printf('%03d', n),
			'automation-stale-node-' || printf('%03d', n),
			'schedule',
			'automation-stale-schedule-' || printf('%03d', n),
			'automation-stale-occurrence-' || printf('%03d', n),
			'completed',
			datetime('2024-01-01'),
			datetime('2024-01-01')
		FROM seq`, automationCount, automationStalePRDiscoveryProjectID); err != nil {
		tb.Fatalf("insert automation invocations: %v", err)
	}
	totalPRs := automationCount * prsPerAutomation
	if _, err := db.ExecContext(ctx, `
		WITH RECURSIVE seq(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM seq WHERE n < ? - 1)
		INSERT INTO tasks (id, project_id, title, category, priority, status, prompt, created_at, updated_at)
		SELECT
			'automation-stale-task-' || printf('%05d', n),
			?,
			'automation stale task ' || printf('%05d', n),
			'completed',
			2,
			'completed',
			'p',
			datetime('2024-01-01'),
			datetime('2024-02-01')
		FROM seq`, totalPRs, automationStalePRDiscoveryProjectID); err != nil {
		tb.Fatalf("insert tasks: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		WITH RECURSIVE seq(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM seq WHERE n < ? - 1)
		INSERT INTO task_pull_requests (task_id, pr_number, pr_url, pr_state, created_at, updated_at)
		SELECT
			'automation-stale-task-' || printf('%05d', n),
			100000 + n,
			'https://github.com/example/runtime/pull/' || (100000 + n),
			'open',
			datetime('2024-01-01'),
			datetime('2024-02-01')
		FROM seq`, totalPRs); err != nil {
		tb.Fatalf("insert task pull requests: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		WITH RECURSIVE seq(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM seq WHERE n < ? - 1)
		INSERT INTO automation_activities (id, project_id, automation_id, version_id, node_id, invocation_id, activity_key, activity_type, status, started_at, completed_at)
		SELECT
			'automation-stale-activity-' || printf('%05d', n),
			?,
			'automation-stale-auto-' || printf('%03d', n / ?),
			'automation-stale-version-' || printf('%03d', n / ?),
			'automation-stale-node-' || printf('%03d', n / ?),
			'automation-stale-invocation-' || printf('%03d', n / ?),
			'automation-stale-pull-open-' || printf('%05d', n),
			'open_pull_request',
			'completed',
			datetime('2024-01-01'),
			datetime('2024-01-01')
		FROM seq`, totalPRs, automationStalePRDiscoveryProjectID, prsPerAutomation, prsPerAutomation, prsPerAutomation, prsPerAutomation); err != nil {
		tb.Fatalf("insert automation activities: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		WITH RECURSIVE seq(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM seq WHERE n < ? - 1)
		INSERT INTO automation_activity_resources (activity_id, resource_type, resource_id, relation)
		SELECT 'automation-stale-activity-' || printf('%05d', n), 'task', 'automation-stale-task-' || printf('%05d', n), 'subject' FROM seq
		UNION ALL
		SELECT 'automation-stale-activity-' || printf('%05d', n), 'pull_request', 'github:example/runtime:pull:' || (100000 + n), 'result' FROM seq`, totalPRs); err != nil {
		tb.Fatalf("insert automation activity resources: %v", err)
	}
}

func markSparseAutomationStalePRs(tb testing.TB, db *sql.DB) {
	tb.Helper()
	ctx := context.Background()
	staleTasks := []string{"automation-stale-task-00600", "automation-stale-task-00601", "automation-stale-task-01400", "automation-stale-task-01800"}
	for _, taskID := range staleTasks {
		if _, err := db.ExecContext(ctx, `UPDATE task_pull_requests SET updated_at = datetime('2024-01-01') WHERE task_id = ?`, taskID); err != nil {
			tb.Fatalf("mark %s stale: %v", taskID, err)
		}
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM automation_activity_resources WHERE activity_id = 'automation-stale-activity-01800' AND resource_type = 'pull_request'`); err != nil {
		tb.Fatalf("remove pull request resource from stale orphan activity: %v", err)
	}
}

func markBenchmarkSparseAutomationStalePRs(tb testing.TB, db *sql.DB, staleAutomations int) {
	tb.Helper()
	if _, err := db.ExecContext(context.Background(), `
		WITH RECURSIVE seq(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM seq WHERE n < ? - 1)
		UPDATE task_pull_requests
		SET updated_at = datetime('2024-01-01')
		WHERE task_id IN (SELECT 'automation-stale-task-' || printf('%05d', n * 1000) FROM seq)`, staleAutomations); err != nil {
		tb.Fatalf("mark benchmark sparse stale PRs: %v", err)
	}
}

func explainAutomationStalePRDiscoveryPlan(tb testing.TB, db *sql.DB) string {
	tb.Helper()
	return explainAutomationStalePRDiscoveryPlanForQuery(tb, db, listAutomationsWithStaleExternalPullRequestsSQL)
}

func explainAutomationStalePRDiscoveryPlanForQuery(tb testing.TB, db *sql.DB, query string) string {
	tb.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, "2024-01-15 00:00:00", 100)
	if err != nil {
		tb.Fatalf("explain stale PR discovery query plan: %v", err)
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
	return strings.Join(details, " | ")
}

func assertAutomationStalePRDiscoveryPlan(tb testing.TB, plan string) {
	tb.Helper()
	prIndex := strings.Index(plan, "idx_task_pull_requests_updated_at_task_id")
	if prIndex < 0 || !strings.Contains(plan, "updated_at<?") {
		tb.Fatalf("plan = %s, want stale updated_at task_pull_requests index lookup", plan)
	}
	resourceIndex := strings.Index(plan, "idx_automation_activity_resources_type_resource_activity")
	if resourceIndex < 0 {
		tb.Fatalf("plan = %s, want covering task resource lookup", plan)
	}
	if resourceIndex < prIndex {
		tb.Fatalf("plan = %s, want task_pull_requests stale lookup before resource joins", plan)
	}
	if !strings.Contains(plan, "idx_automation_activity_resources_activity_type") {
		tb.Fatalf("plan = %s, want activity/resource-type pull request existence lookup", plan)
	}
	if strings.Contains(plan, "USE TEMP B-TREE FOR GROUP BY") {
		tb.Fatalf("plan = %s, want no grouped scan over tracked pull request resources", plan)
	}
	if strings.Contains(plan, "idx_automation_activity_resources_reverse (resource_type=?)") {
		tb.Fatalf("plan = %s, want no scan of all tracked resources by resource_type", plan)
	}
}
