package repository

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/testutil"
)

func TestProjectRepoListWorkerCapacityProjectsUsesCompactProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewProjectRepo(db)
	ctx := context.Background()

	largeDescription := strings.Repeat("project description ", 2048)
	largePath := "/private/worktrees/" + strings.Repeat("local-repository-path/", 512)
	largeURL := "https://github.example.test/acme/" + strings.Repeat("remote-repository-url/", 512)
	if _, err := db.ExecContext(ctx, `UPDATE projects
		SET description = ?, repo_path = ?, repo_url = ?, max_workers = ?
		WHERE id = 'default'`, largeDescription, largePath, largeURL, 4); err != nil {
		t.Fatalf("seed default project: %v", err)
	}
	seedWorkerCapacityProject(t, db, "capacity-alpha", "Alpha", largeDescription, largePath, largeURL, 2)
	seedWorkerCapacityProject(t, db, "capacity-zeta-2", "Zeta", largeDescription, largePath, largeURL, nil)
	seedWorkerCapacityProject(t, db, "capacity-zeta-1", "Zeta", largeDescription, largePath, largeURL, 7)

	counter.Reset()
	counter.SetEnabled(true)
	capacities, err := repo.ListWorkerCapacityProjects(ctx)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("ListWorkerCapacityProjects: %v", err)
	}
	if len(capacities) < 4 {
		t.Fatalf("capacity project count = %d, want at least 4", len(capacities))
	}
	if capacities[0].ID != "default" {
		t.Fatalf("first capacity project = %q, want default project first", capacities[0].ID)
	}

	byID := make(map[string]struct {
		name  string
		limit *int
	}, len(capacities))
	for _, capacity := range capacities {
		byID[capacity.ID] = struct {
			name  string
			limit *int
		}{capacity.Name, capacity.MaxWorkers}
	}
	if got := byID["capacity-alpha"]; got.name != "Alpha" || got.limit == nil || *got.limit != 2 {
		t.Fatalf("Alpha capacity = %+v, want name Alpha and limit 2", got)
	}
	if got := byID["capacity-zeta-2"]; got.limit != nil {
		t.Fatalf("unlimited capacity limit = %v, want nil", got.limit)
	}

	full, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List full projects: %v", err)
	}
	if len(full) != len(capacities) {
		t.Fatalf("full/capacity project counts = %d/%d", len(full), len(capacities))
	}
	for i := range full {
		if capacities[i].ID != full[i].ID {
			t.Fatalf("capacity ordering diverged at %d: compact=%q full=%q", i, capacities[i].ID, full[i].ID)
		}
	}

	defaultProject, err := repo.GetByID(ctx, "default")
	if err != nil || defaultProject == nil {
		t.Fatalf("GetByID default: project=%v err=%v", defaultProject, err)
	}
	if defaultProject.Description != largeDescription || defaultProject.RepoPath != largePath || defaultProject.RepoURL != largeURL {
		t.Fatal("full project read did not retain metadata omitted from the capacity projection")
	}

	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("capacity statements = %d, want 1: %q", len(statements), statements)
	}
	if got, want := normalizeProjectQuery(statements[0]), normalizeProjectQuery(projectWorkerCapacityProjectsQuery); got != want {
		t.Fatalf("capacity query = %q, want %q", got, want)
	}
}

func TestProjectRepoListWorkerCapacityProjectsQueryPlan(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+projectWorkerCapacityProjectsQuery)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		plan.WriteString(detail)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("query plan rows: %v", err)
	}
	planText := strings.ToUpper(plan.String())
	if strings.Contains(planText, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("worker-capacity query uses a temporary ORDER BY sort:\n%s", plan.String())
	}
	if !strings.Contains(planText, "IDX_PROJECTS_SELECTOR_ORDER") {
		t.Fatalf("worker-capacity query does not use the project selector order index:\n%s", plan.String())
	}
}

func seedWorkerCapacityProject(t *testing.T, db *sql.DB, id, name, description, repoPath, repoURL string, maxWorkers any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO projects (id, name, description, repo_path, repo_url, max_workers)
		 VALUES (?, ?, ?, ?, ?, ?)`, id, name, description, repoPath, repoURL, maxWorkers); err != nil {
		t.Fatalf("seed capacity project %q: %v", name, err)
	}
}

func normalizeProjectQuery(query string) string {
	return strings.ToLower(strings.Join(strings.Fields(query), " "))
}
