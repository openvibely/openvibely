package repository_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/database"
	"github.com/openvibely/openvibely/internal/repository"
)

// BenchmarkTemplateRepo_ListByProject compares the full-row template query
// (as ListByProject/ListByCategory/Search/ListFavorites/ListRecentlyUsed used
// to select, including default_prompt) against the bounded card projection
// those methods use today. Only the card-shaped card data (name, category,
// priority, favorite state, usage count) is ever rendered by the Templates
// dashboard, so the full-row query is kept here only as the "before" baseline
// for comparison.
func BenchmarkTemplateRepo_ListByProject(b *testing.B) {
	fixtures := []struct {
		name          string
		templateCount int
		promptSize    int
	}{
		{name: "Small20x512B", templateCount: 20, promptSize: 512},
		{name: "Large200x4KiB", templateCount: 200, promptSize: 4 * 1024},
	}

	for _, fixture := range fixtures {
		b.Run(fixture.name, func(b *testing.B) {
			db := newTemplateBenchmarkDB(b, fixture.templateCount, fixture.promptSize)
			defer db.Close()
			repo := repository.NewTemplateRepo(db)

			b.Run("FullRowBaseline", func(b *testing.B) {
				benchmarkTemplateFullRowQuery(b, db)
			})
			b.Run("CardProjection", func(b *testing.B) {
				benchmarkTemplateListByProject(b, repo)
			})
		})
	}
}

func benchmarkTemplateListByProject(b *testing.B, repo *repository.TemplateRepo) {
	b.Helper()
	ctx := context.Background()
	var promptBytes int64

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		templates, err := repo.ListByProject(ctx, "default")
		if err != nil {
			b.Fatalf("list templates: %v", err)
		}
		promptBytes = 0
		for _, tmpl := range templates {
			promptBytes += int64(len(tmpl.DefaultPrompt))
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(promptBytes), "prompt_bytes/op")
}

// benchmarkTemplateFullRowQuery replays the pre-fix query shape (selecting
// default_prompt for every row) directly against the fixture database, since
// the repository no longer exposes that broad projection in production code.
func benchmarkTemplateFullRowQuery(b *testing.B, db *sql.DB) {
	b.Helper()
	ctx := context.Background()
	var promptBytes int64

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := db.QueryContext(ctx, `
			SELECT id, project_id, name, description, default_prompt, suggested_agent_id,
				category, priority, tag, tags_json, category_filter,
				is_built_in, is_favorite, usage_count, created_by, created_at, updated_at
			FROM task_templates
			WHERE project_id = ? OR project_id IS NULL
			ORDER BY is_favorite DESC, usage_count DESC, name ASC`,
			"default",
		)
		if err != nil {
			b.Fatalf("query full template rows: %v", err)
		}
		promptBytes = 0
		for rows.Next() {
			var (
				id, name, description, defaultPrompt, category, tag, tagsJSON, categoryFilter, createdBy string
				projectID, agentID                                                                       sql.NullString
				priority, isBuiltIn, isFavorite, usageCount                                              int
				createdAt, updatedAt                                                                     string
			)
			if err := rows.Scan(
				&id, &projectID, &name, &description, &defaultPrompt, &agentID,
				&category, &priority, &tag, &tagsJSON, &categoryFilter,
				&isBuiltIn, &isFavorite, &usageCount, &createdBy, &createdAt, &updatedAt,
			); err != nil {
				rows.Close()
				b.Fatalf("scan full template row: %v", err)
			}
			promptBytes += int64(len(defaultPrompt))
		}
		if err := rows.Err(); err != nil {
			b.Fatalf("iterate full template rows: %v", err)
		}
		rows.Close()
	}
	b.StopTimer()
	b.ReportMetric(float64(promptBytes), "prompt_bytes/op")
}

func newTemplateBenchmarkDB(b *testing.B, templateCount, promptSize int) *sql.DB {
	b.Helper()
	b.StopTimer()
	db, err := database.New(":memory:")
	if err != nil {
		b.Fatalf("create benchmark database: %v", err)
	}
	prompt := strings.Repeat("p", promptSize)
	tx, err := db.Begin()
	if err != nil {
		db.Close()
		b.Fatalf("begin fixture transaction: %v", err)
	}
	stmt, err := tx.Prepare(`
		INSERT INTO task_templates (
			id, project_id, name, description, default_prompt, category,
			priority, tag, tags_json, category_filter, is_built_in, is_favorite,
			usage_count, created_by
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		db.Close()
		b.Fatalf("prepare fixture insert: %v", err)
	}
	defer stmt.Close()

	for i := 0; i < templateCount; i++ {
		id := fmt.Sprintf("benchmark-template-%03d", i)
		name := fmt.Sprintf("Benchmark Template %03d", templateCount-i)
		description := "Benchmark fixture template"
		isFavorite := 0
		if i%5 == 0 {
			isFavorite = 1
		}
		var projectID any
		if i%3 == 0 {
			projectID = "default"
		}
		if _, err := stmt.Exec(
			id, projectID, name, description, prompt, "backlog",
			(i%4)+1, "feature", "[]", "all", 0, isFavorite, i%10, "user",
		); err != nil {
			tx.Rollback()
			db.Close()
			b.Fatalf("insert fixture template %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		db.Close()
		b.Fatalf("commit fixture transaction: %v", err)
	}
	return db
}
