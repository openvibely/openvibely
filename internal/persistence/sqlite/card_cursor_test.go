package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/cardcursor"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestCardCursorSQLMatchesContinuation(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := &models.Project{Name: "Project 1"}
	require.NoError(t, NewProjectRepo(db).Create(ctx, project))
	agents := NewAgentRepo(db)
	configs := NewLLMConfigRepo(db)
	hooks := NewWebhookRepo(db)
	alerts := NewAlertRepo(db)
	automations := NewAutomationRepo(db)
	for i, name := range []string{"Alpha", "alpha", "Alpha", "Zulu", "Éclair", "éclair", "Beta"} {
		agent := &models.Agent{Name: name, Model: "inherit"}
		require.NoError(t, agents.Create(ctx, agent))
		require.NoError(t, configs.Create(ctx, &models.LLMConfig{Name: fmt.Sprintf("%s %d", name, i), Provider: models.ProviderTest, Model: fmt.Sprint(i)}))
		require.NoError(t, hooks.Create(ctx, &models.WebhookEndpoint{ProjectID: project.ID, Name: name, Enabled: true}))
		severity := []models.AlertSeverity{models.SeverityError, models.SeverityWarning, models.SeverityInfo}[i%3]
		alert := &models.Alert{ProjectID: project.ID, Title: name, Type: models.AlertCustom, Severity: severity, IsRead: i%2 == 0}
		require.NoError(t, alerts.Create(ctx, alert))
		id, version := persistence.NewID(), persistence.NewID()
		_, err := db.Exec(`INSERT INTO automations (id,project_id,stable_key,name,automation_type,lifecycle_state,health_state,created_via) VALUES (?,?,?,?,'custom','active','unknown','test')`, id, project.ID, fmt.Sprint(i), name)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO automation_versions (id,project_id,automation_id,version,state,source,adapter_key,published_at) VALUES (?,?,?,1,'published','manual','custom',CURRENT_TIMESTAMP)`, version, project.ID, id)
		require.NoError(t, err)
		_, err = db.Exec(`UPDATE automations SET published_version_id=? WHERE id=?`, version, id)
		require.NoError(t, err)
		stamp := time.Date(2026, 1, 1, 0, i/2, 0, 123456789+i%2, time.UTC)
		for table, rid := range map[string]string{"agents": agent.ID, "alerts": alert.ID, "automations": id} {
			_, err = db.Exec(`UPDATE `+table+` SET created_at=?,updated_at=? WHERE id=?`, stamp, stamp, rid)
			require.NoError(t, err)
		}
	}
	for _, kind := range []string{"agents", "models", "webhooks", "alerts", "automations"} {
		orders := []string{"name_asc", "name_desc"}
		if kind == "agents" {
			orders = append(orders, "updated_desc", "created_desc")
		}
		if kind == "automations" {
			orders = append(orders, "updated_desc", "updated_asc")
		}
		if kind == "models" {
			orders = append(orders, "provider")
		}
		if kind == "alerts" {
			orders = []string{"newest", "oldest", "severity", "unread_first"}
		}
		for _, order := range orders {
			t.Run(kind+"/"+order, func(t *testing.T) {
				fetch := func(ctx context.Context, limit int) []any {
					var result []any
					switch kind {
					case "agents":
						rows, err := agents.ListPageFiltered(ctx, limit, 0, persistence.AgentPageFilter{Sort: order})
						require.NoError(t, err)
						for _, r := range rows {
							result = append(result, r)
						}
					case "models":
						rows, err := configs.ListCardsPageFiltered(ctx, limit, 0, persistence.ModelCardListFilter{Sort: order})
						require.NoError(t, err)
						for _, r := range rows {
							result = append(result, r)
						}
					case "webhooks":
						rows, err := hooks.ListCardsByProjectPageFiltered(ctx, project.ID, limit, 0, persistence.WebhookCardFilter{Sort: order})
						require.NoError(t, err)
						for _, r := range rows {
							result = append(result, r)
						}
					case "automations":
						rows, err := automations.ListPortfolioCardsPageFiltered(ctx, project.ID, limit, 0, persistence.AutomationCardListFilter{Sort: order})
						require.NoError(t, err)
						for _, r := range rows {
							result = append(result, r)
						}
					case "alerts":
						rows, err := alerts.ListFilteredSummaries(ctx, project.ID, models.AlertListFilter{Limit: limit, Sort: order})
						require.NoError(t, err)
						for _, r := range rows {
							result = append(result, r)
						}
					}
					return result
				}
				expected := fetch(cardcursor.With(ctx, cardcursor.New(kind, order, "test", "")), 50)
				var got []any
				token := ""
				for n := 0; n < 20; n++ {
					state := cardcursor.New(kind, order, "test", token)
					require.NoError(t, state.Err)
					pageCtx := cardcursor.With(ctx, state)
					candidates := fetch(pageCtx, 3)
					page, more, err := cardcursor.Finish(pageCtx, candidates, 2)
					require.NoError(t, err)
					got = append(got, page...)
					token = cardcursor.Encoded(pageCtx)
					if !more {
						break
					}
				}
				require.Equal(t, expected, got)
			})
		}
	}
}
