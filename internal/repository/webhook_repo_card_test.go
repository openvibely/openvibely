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

func TestWebhookRepo_ListCardsByProjectUsesCompactOrderedIndex(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewWebhookRepo(db)
	projectRepo := NewProjectRepo(db)
	project := createWebhookTestProject(t, projectRepo)
	otherProject := createWebhookTestProject(t, projectRepo)

	seedWebhookCardFixture(t, db, project.ID, 200)
	seedWebhookCardFixture(t, db, otherProject.ID, 25)

	query := `SELECT ` + webhookCardColumns + ` FROM webhook_endpoints WHERE project_id = ? ORDER BY name ASC, id ASC`
	plan := webhookExplainQueryPlan(t, db, query, project.ID)
	if !strings.Contains(plan, "idx_webhook_endpoints_project_name_id") {
		t.Fatalf("card list plan = %s, want project/name/id index", plan)
	}
	if strings.Contains(plan, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("card list plan = %s, want no temporary order sort", plan)
	}

	cards, err := repo.ListCardsByProject(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("ListCardsByProject: %v", err)
	}
	if len(cards) != 200 {
		t.Fatalf("card count = %d, want 200", len(cards))
	}
	for i, card := range cards {
		if card.ProjectID != project.ID {
			t.Fatalf("card %d project = %q, want target project", i, card.ProjectID)
		}
		if i > 0 && (card.Name < cards[i-1].Name || (card.Name == cards[i-1].Name && card.ID < cards[i-1].ID)) {
			t.Fatalf("card %d out of name/id order", i)
		}
		if card.Secret != "" || card.SystemInstructions != "" || card.TitleTemplate != "" || card.PromptTemplate != "" {
			t.Fatalf("card %d carried edit-only payloads: %#v", i, card)
		}
		if card.PathToken == "" || card.DefaultPriority == 0 || card.CreatedAt.IsZero() || card.UpdatedAt.IsZero() {
			t.Fatalf("card %d missing visible/action fields: %#v", i, card)
		}
	}
}

func BenchmarkWebhookSettingsListCards200(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := NewWebhookRepo(db)
	projectRepo := NewProjectRepo(db)
	project := &models.Project{Name: "webhook-card-bench"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		b.Fatalf("creating test project: %v", err)
	}
	seedWebhookCardFixture(b, db, project.ID, 200)

	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cards, err := repo.ListCardsByProject(ctx, project.ID)
		if err != nil {
			b.Fatalf("ListCardsByProject: %v", err)
		}
		if len(cards) != 200 {
			b.Fatalf("card count = %d, want 200", len(cards))
		}
	}
}

func seedWebhookCardFixture(tb testing.TB, db *sql.DB, projectID string, count int) {
	tb.Helper()
	large := strings.Repeat("x", 32*1024)
	for i := 0; i < count; i++ {
		_, err := db.Exec(`
			INSERT INTO webhook_endpoints
				(id, project_id, name, enabled, path_token, secret, system_instructions, title_template, prompt_template, default_priority)
			VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?, ?)`,
			fmt.Sprintf("wh-%s-%04d", projectID, i),
			projectID,
			fmt.Sprintf("Webhook %04d", count-i),
			fmt.Sprintf("token-%s-%04d", projectID, i),
			strings.Repeat("s", 128),
			large,
			large,
			large,
			(i%4)+1,
		)
		if err != nil {
			tb.Fatalf("seed webhook %d: %v", i, err)
		}
	}
}

func webhookExplainQueryPlan(tb testing.TB, db *sql.DB, query string, args ...any) string {
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
	return strings.Join(details, "; ")
}
