package sqlite_test

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/internal/testutil/sqlitefixtures"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

// SQL shape belongs to SQLite; application tests assert physical read counts and returned scope.
func TestAutomationRuntimeCardSQLRemainsBoundedAndDirect(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	stores := sqlite.NewStores(db, nil)
	fixtures := sqlitefixtures.New(db)
	ctx := t.Context()
	require.NoError(t, fixtures.SeedAutomation(ctx, models.Automation{ID: "runtime-card", ProjectID: "default", StableKey: "runtime-card", Name: "Runtime card", AutomationType: "custom", LifecycleState: "active"}))
	require.NoError(t, fixtures.SeedAutomationVersion(ctx, models.AutomationVersion{ID: "runtime-version", ProjectID: "default", AutomationID: "runtime-card", Version: 1, State: "published", Source: "manual", AdapterKey: "custom"}))
	require.NoError(t, fixtures.SetAutomationPublishedVersion(ctx, "runtime-card", "runtime-version"))
	counter.Reset()
	counter.SetEnabled(true)
	cards, _, err := stores.Automations.ListRuntimeAutomationCards(ctx, "default", 20, 0)
	require.NoError(t, err)
	_, err = stores.Automations.PortfolioOperationalCountsForAutomations(ctx, "default", []string{"runtime-card"}, time.Now().UTC().Add(-24*time.Hour))
	counter.SetEnabled(false)
	require.NoError(t, err)
	require.Len(t, cards, 1)
	statements := strings.ToLower(strings.Join(counter.Statements(), "\n"))
	require.Contains(t, statements, "limit ? offset ?")
	require.Contains(t, statements, "selected_automations(automation_id) as (values")
	require.NotContains(t, statements, "listing automation portfolio cards")
	counter.Reset()
	counter.SetEnabled(true)
	card, err := stores.Automations.GetRuntimeAutomationCard(ctx, "default", "runtime-card")
	require.NoError(t, err)
	_, err = stores.Automations.PortfolioOperationalCountsForAutomations(ctx, "default", []string{"runtime-card"}, time.Now().UTC().Add(-24*time.Hour))
	counter.SetEnabled(false)
	require.NoError(t, err)
	require.NotNil(t, card)
	statements = strings.ToLower(strings.Join(counter.Statements(), "\n"))
	require.Contains(t, statements, "where a.project_id = ? and a.id = ?")
	require.Contains(t, statements, "selected_automations(automation_id) as (values (?))")
	require.NotContains(t, statements, "order by a.updated_at desc, a.id limit ?")
	counter.Reset()
	counter.SetEnabled(true)
	exists, err := stores.Automations.Exists(ctx, "default", "runtime-card")
	counter.SetEnabled(false)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, []string{"SELECT EXISTS(SELECT 1 FROM automations WHERE project_id = ? AND id = ?)"}, counter.Statements())
}
