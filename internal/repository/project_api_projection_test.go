package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/testutil"
)

func TestProjectRepo_ListAPIProjectsUsesCompactProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewProjectRepo(db)
	ctx := context.Background()

	description := strings.Repeat("description-", 4096)
	repoPath := "/workspace/" + strings.Repeat("repository/", 64)
	repoURL := "https://github.example.test/" + strings.Repeat("repository/", 64)
	seedSelectorProject(t, db, "api-compact", "API Compact", false, description, repoPath, repoURL)
	if _, err := db.ExecContext(ctx, `UPDATE projects SET created_at = ?, updated_at = ? WHERE id = ?`,
		"2024-01-02 03:04:05", "2024-01-03 04:05:06", "api-compact"); err != nil {
		t.Fatalf("set project timestamps: %v", err)
	}

	counter.SetEnabled(true)
	counter.Reset()
	projects, err := repo.ListAPIProjects(ctx)
	if err != nil {
		t.Fatalf("ListAPIProjects: %v", err)
	}
	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("ListAPIProjects statements = %d, want 1: %#v", len(statements), statements)
	}
	query := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
	if query != "select id, name, repo_path, created_at from projects order by is_default desc, name asc" {
		t.Fatalf("ListAPIProjects query = %q", query)
	}
	for _, omitted := range []string{"description", "repo_url", "default_agent_config_id", "max_workers", "updated_at"} {
		if strings.Contains(query, omitted) {
			t.Errorf("compact API query selected omitted column %q: %s", omitted, query)
		}
	}

	var compactFound bool
	for _, project := range projects {
		if project.ID != "api-compact" {
			continue
		}
		compactFound = true
		if project.Name != "API Compact" || project.RepoPath != repoPath {
			t.Fatalf("compact project identity/path = %+v", project)
		}
		if got := project.CreatedAt.Format("2006-01-02T15:04:05Z07:00"); got != "2024-01-02T03:04:05Z" {
			t.Fatalf("compact project created_at = %q", got)
		}
	}
	if !compactFound {
		t.Fatal("compact API projection did not return api-compact")
	}

	full, err := repo.GetByID(ctx, "api-compact")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if full == nil || full.Description != description || full.RepoPath != repoPath || full.RepoURL != repoURL {
		t.Fatalf("full project read lost omitted fields: %+v", full)
	}
}

func TestProjectRepo_ListAPIProjectsOrderingMatchesFullList(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewProjectRepo(db)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `UPDATE projects SET is_default = 0`); err != nil {
		t.Fatalf("clear default project flag: %v", err)
	}
	seedSelectorProject(t, db, "api-default", "Zebra Default", true, "", "/default/path", "")
	seedSelectorProject(t, db, "api-alpha", "Alpha", false, "", "/alpha/path", "")
	seedSelectorProject(t, db, "api-same-b", "Same", false, "", "/same-b/path", "")
	seedSelectorProject(t, db, "api-same-a", "Same", false, "", "/same-a/path", "")

	full, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	compact, err := repo.ListAPIProjects(ctx)
	if err != nil {
		t.Fatalf("ListAPIProjects: %v", err)
	}
	if len(full) != len(compact) {
		t.Fatalf("full rows = %d, compact rows = %d", len(full), len(compact))
	}
	for i := range full {
		if full[i].ID != compact[i].ID || full[i].Name != compact[i].Name || full[i].RepoPath != compact[i].RepoPath || !full[i].CreatedAt.Equal(compact[i].CreatedAt) {
			t.Fatalf("ordering/values differ at %d: full=%+v compact=%+v", i, full[i], compact[i])
		}
	}
	if compact[0].ID != "api-default" {
		t.Fatalf("default project was not first: %q", compact[0].ID)
	}
	for i := 2; i < len(compact); i++ {
		if compact[i-1].Name > compact[i].Name {
			t.Fatalf("non-default names are not ascending at %d: %q > %q", i, compact[i-1].Name, compact[i].Name)
		}
	}
}

func TestProjectRepo_ListAPIProjectsQueryPlanUsesSelectorOrderIndex(t *testing.T) {
	db := testutil.NewTestDB(t)
	rows, err := db.QueryContext(context.Background(), `EXPLAIN QUERY PLAN `+projectAPIProjectsQuery)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		plan.WriteString(detail)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("query plan rows: %v", err)
	}
	planText := plan.String()
	if !strings.Contains(planText, "idx_projects_selector_order") {
		t.Fatalf("API project query did not use selector-order index: %s", planText)
	}
	if strings.Contains(planText, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("API project query uses a temporary order B-tree: %s", planText)
	}
}

func TestProjectRepo_ListAPIProjectsEmpty(t *testing.T) {
	db := testutil.NewTestDB(t)
	if _, err := db.ExecContext(context.Background(), `DELETE FROM projects`); err != nil {
		t.Fatalf("delete projects: %v", err)
	}
	projects, err := NewProjectRepo(db).ListAPIProjects(context.Background())
	if err != nil {
		t.Fatalf("ListAPIProjects: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("ListAPIProjects returned %d rows for an empty workspace", len(projects))
	}
}
