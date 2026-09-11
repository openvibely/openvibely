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

	enabled := true
	for _, test := range []struct {
		name  string
		sort  string
		order string
	}{
		{name: "ascending", sort: "name_asc", order: "ASC"},
		{name: "descending", sort: "name_desc", order: "DESC"},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := `SELECT ` + webhookCardColumns + ` FROM webhook_endpoints
				WHERE project_id = ? AND enabled = ?
				ORDER BY name COLLATE NOCASE ` + test.order + `, name ` + test.order + `, id ` + test.order + ` LIMIT ? OFFSET ?`
			plan := webhookExplainQueryPlan(t, db, query, project.ID, true, 20, 20)
			if !strings.Contains(plan, "idx_webhook_endpoints_project_name_id") {
				t.Fatalf("paginated card list plan = %s, want project/name/id index", plan)
			}
			if strings.Contains(plan, "USE TEMP B-TREE FOR ORDER BY") {
				t.Fatalf("paginated card list plan = %s, want no temporary order sort", plan)
			}

			cards, err := repo.ListCardsByProjectPageFiltered(context.Background(), project.ID, 20, 20, WebhookCardFilter{Enabled: &enabled, Sort: test.sort})
			if err != nil {
				t.Fatalf("ListCardsByProjectPageFiltered: %v", err)
			}
			if len(cards) != 20 {
				t.Fatalf("card count = %d, want 20", len(cards))
			}
		})
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

func TestWebhookRepo_ListCardsByProjectUsesSQLiteNoCaseOrderingForUnicodeNames(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewWebhookRepo(db)
	project := createWebhookTestProject(t, NewProjectRepo(db))
	for _, name := range []string{"Zulu webhook", "Äz webhook", "äa webhook", "Alpha webhook"} {
		if err := repo.Create(context.Background(), &models.WebhookEndpoint{ProjectID: project.ID, Name: name, Enabled: true, DefaultPriority: 1}); err != nil {
			t.Fatalf("create webhook %q: %v", name, err)
		}
	}

	for _, test := range []struct {
		name string
		sort string
		want []string
	}{
		{name: "ascending", sort: "name_asc", want: []string{"Alpha webhook", "Zulu webhook", "Äz webhook", "äa webhook"}},
		{name: "descending", sort: "name_desc", want: []string{"äa webhook", "Äz webhook", "Zulu webhook", "Alpha webhook"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got []string
			for offset := 0; offset < 4; offset += 2 {
				cards, err := repo.ListCardsByProjectPageFiltered(context.Background(), project.ID, 2, offset, WebhookCardFilter{Sort: test.sort})
				if err != nil {
					t.Fatalf("ListCardsByProjectPageFiltered offset %d: %v", offset, err)
				}
				for _, card := range cards {
					got = append(got, card.Name)
				}
			}
			if fmt.Sprint(got) != fmt.Sprint(test.want) {
				t.Fatalf("names = %q, want %q", got, test.want)
			}
		})
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
