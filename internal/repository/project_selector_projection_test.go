package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

// seedSelectorProject inserts a project row directly so tests can control
// is_default, large text fields, and equal names precisely.
func seedSelectorProject(t *testing.T, db *sql.DB, id, name string, isDefault bool, description, repoPath, repoURL string) {
	t.Helper()
	def := 0
	if isDefault {
		def = 1
	}
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO projects (id, name, description, repo_path, repo_url, is_default) VALUES (?, ?, ?, ?, ?, ?)`,
		id, name, description, repoPath, repoURL, def)
	if err != nil {
		t.Fatalf("seed project %q: %v", name, err)
	}
}

// TestProjectRepo_ListSelectorOptions_Compact verifies the selector projection
// returns only the fields the shared shell renders and does not carry the large
// description/repository payloads a full row would.
func TestProjectRepo_ListSelectorOptions_Compact(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewProjectRepo(db)
	ctx := context.Background()

	bigDesc := strings.Repeat("D", 16*1024)
	bigPath := strings.Repeat("p", 2*1024)
	bigURL := strings.Repeat("u", 2*1024)
	seedSelectorProject(t, db, "p-alpha", "Alpha", false, bigDesc, bigPath, bigURL)

	opts, err := repo.ListSelectorOptions(ctx)
	if err != nil {
		t.Fatalf("ListSelectorOptions: %v", err)
	}

	var alpha *models.Project
	for i := range opts {
		if opts[i].ID == "p-alpha" {
			alpha = &opts[i]
		}
	}
	if alpha == nil {
		t.Fatal("expected 'Alpha' project in selector options")
	}
	if alpha.Name != "Alpha" {
		t.Errorf("expected Name=Alpha, got %q", alpha.Name)
	}
	// Compact projection must not materialize the heavy/omitted columns.
	if alpha.Description != "" {
		t.Errorf("expected empty Description in compact projection, got %d bytes", len(alpha.Description))
	}
	if alpha.RepoPath != "" {
		t.Errorf("expected empty RepoPath in compact projection, got %d bytes", len(alpha.RepoPath))
	}
	if alpha.RepoURL != "" {
		t.Errorf("expected empty RepoURL in compact projection, got %d bytes", len(alpha.RepoURL))
	}
	if alpha.DefaultAgentConfigID != nil {
		t.Errorf("expected nil DefaultAgentConfigID, got %v", alpha.DefaultAgentConfigID)
	}
	if alpha.MaxWorkers != nil {
		t.Errorf("expected nil MaxWorkers, got %v", alpha.MaxWorkers)
	}
	if !alpha.CreatedAt.IsZero() {
		t.Errorf("expected zero CreatedAt, got %v", alpha.CreatedAt)
	}
	if !alpha.UpdatedAt.IsZero() {
		t.Errorf("expected zero UpdatedAt, got %v", alpha.UpdatedAt)
	}

	// The authoritative full-record read still returns the omitted fields.
	full, err := repo.GetByID(ctx, "p-alpha")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if len(full.Description) != len(bigDesc) || len(full.RepoPath) != len(bigPath) || len(full.RepoURL) != len(bigURL) {
		t.Errorf("expected GetByID to return full payloads, got desc=%d path=%d url=%d",
			len(full.Description), len(full.RepoPath), len(full.RepoURL))
	}
}

// TestProjectRepo_ListSelectorOptions_Ordering verifies default project first,
// then name ascending, with a deterministic id tie-breaker for equal names.
func TestProjectRepo_ListSelectorOptions_Ordering(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewProjectRepo(db)
	ctx := context.Background()

	// Seeded default project id='default' name='Default' already exists.
	seedSelectorProject(t, db, "id-b", "Beta", false, "", "", "")
	seedSelectorProject(t, db, "id-a", "Alpha", false, "", "", "")
	// Two projects with equal names to prove the id tie-breaker is deterministic.
	seedSelectorProject(t, db, "dup-2", "Zeta", false, "", "", "")
	seedSelectorProject(t, db, "dup-1", "Zeta", false, "", "", "")

	opts, err := repo.ListSelectorOptions(ctx)
	if err != nil {
		t.Fatalf("ListSelectorOptions: %v", err)
	}

	if len(opts) < 5 {
		t.Fatalf("expected at least 5 projects, got %d", len(opts))
	}
	// Default project must come first.
	if !opts[0].IsDefault {
		t.Errorf("expected first project to be default, got %+v", opts[0])
	}
	if opts[0].ID != "default" {
		t.Errorf("expected default project id 'default' first, got %q", opts[0].ID)
	}

	// Collect the non-default order.
	var names []string
	var zetaIDs []string
	for _, o := range opts {
		if o.IsDefault {
			continue
		}
		names = append(names, o.Name)
		if o.Name == "Zeta" {
			zetaIDs = append(zetaIDs, o.ID)
		}
	}
	// Alpha before Beta before Zeta.
	assertOrder(t, names, "Alpha", "Beta")
	assertOrder(t, names, "Beta", "Zeta")
	// Equal-name tie-break must be ascending id: dup-1 before dup-2.
	if len(zetaIDs) != 2 || zetaIDs[0] != "dup-1" || zetaIDs[1] != "dup-2" {
		t.Errorf("expected deterministic Zeta id order [dup-1 dup-2], got %v", zetaIDs)
	}

	// The compact ordering must match List's default-first, name-asc contract.
	full, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var fullOrder, compactOrder []string
	for _, p := range full {
		fullOrder = append(fullOrder, p.Name)
	}
	for _, p := range opts {
		compactOrder = append(compactOrder, p.Name)
	}
	if strings.Join(fullOrder, ",") != strings.Join(compactOrder, ",") {
		t.Errorf("selector ordering diverged from List:\n full=%v\n compact=%v", fullOrder, compactOrder)
	}
}

func assertOrder(t *testing.T, names []string, first, second string) {
	t.Helper()
	fi, si := -1, -1
	for i, n := range names {
		if n == first && fi == -1 {
			fi = i
		}
		if n == second {
			si = i
		}
	}
	if fi == -1 || si == -1 || fi > si {
		t.Errorf("expected %q before %q in %v", first, second, names)
	}
}

// TestProjectRepo_ListSelectorOptions_QueryPlan asserts the order-covering index
// satisfies the selector ORDER BY without a temp B-tree sort.
func TestProjectRepo_ListSelectorOptions_QueryPlan(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	rows, err := db.QueryContext(ctx,
		`EXPLAIN QUERY PLAN
		 SELECT id, name, is_default
		 FROM projects ORDER BY is_default DESC, name ASC, id ASC`)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan.WriteString(detail)
		plan.WriteString("\n")
	}
	planText := plan.String()
	if strings.Contains(planText, "USE TEMP B-TREE FOR ORDER BY") {
		t.Errorf("selector query plan uses a temp B-tree sort:\n%s", planText)
	}
	if !strings.Contains(planText, "idx_projects_selector_order") {
		t.Errorf("selector query plan does not use idx_projects_selector_order:\n%s", planText)
	}
}

// TestProjectRepo_ListSelectorOptions_Empty verifies an empty workspace returns
// no rows without error (relevant to fresh installs before the default seed).
func TestProjectRepo_ListSelectorOptions_Empty(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DELETE FROM projects`); err != nil {
		t.Fatalf("delete projects: %v", err)
	}
	repo := NewProjectRepo(db)
	opts, err := repo.ListSelectorOptions(ctx)
	if err != nil {
		t.Fatalf("ListSelectorOptions: %v", err)
	}
	if len(opts) != 0 {
		t.Errorf("expected 0 options for empty workspace, got %d", len(opts))
	}
}

// benchSeedProjects inserts n production-shaped projects with large text fields.
func benchSeedProjects(b *testing.B, db *sql.DB, n int) {
	b.Helper()
	bigDesc := strings.Repeat("D", 16*1024)
	bigPath := strings.Repeat("p", 2*1024)
	bigURL := strings.Repeat("u", 2*1024)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		b.Fatalf("begin: %v", err)
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO projects (id, name, description, repo_path, repo_url, is_default) VALUES (?, ?, ?, ?, ?, 0)`)
	if err != nil {
		b.Fatalf("prepare: %v", err)
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("bench-%06d", i)
		name := fmt.Sprintf("Project %06d", i)
		if _, err := stmt.ExecContext(ctx, id, name, bigDesc, bigPath, bigURL); err != nil {
			b.Fatalf("insert %d: %v", i, err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		b.Fatalf("commit: %v", err)
	}
}

func BenchmarkProjectRepoListForSidebar(b *testing.B) {
	db := testutil.NewTestDB(b)
	benchSeedProjects(b, db, 500)
	repo := NewProjectRepo(db)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		projects, err := repo.ListSelectorOptions(ctx)
		if err != nil {
			b.Fatalf("ListSelectorOptions: %v", err)
		}
		if len(projects) < 500 {
			b.Fatalf("expected >=500 projects, got %d", len(projects))
		}
	}
}
