package sqlite_test

import (
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil"
	"strings"
	"testing"
)

func TestVisionSelectionExactProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := sqlite.NewStores(db, nil).LLMConfigs
	counter.SetEnabled(true)
	if _, err := repo.ListVisionSelectionOptions(t.Context()); err != nil {
		t.Fatal(err)
	}
	counter.SetEnabled(false)
	assertCompactVisionSelectionStatement(t, counter.Statements())
}
func assertCompactVisionSelectionStatement(t *testing.T, statements []string) {
	t.Helper()
	if len(statements) != 1 {
		t.Fatalf("vision routing statements = %#v, want one compact selection query", statements)
	}
	stmt := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
	projection := strings.Split(stmt, " from agent_configs ")[0]
	wantProjection := "select id, name, provider, model, auth_method, is_default, case when coalesce(api_key, '') != '' then 1 else 0 end, case when coalesce(oauth_connection_id, '') != '' then exists(select 1 from oauth_connections c where c.id = oauth_connection_id and c.oauth_access_token != '') else coalesce(oauth_access_token, '') != '' end"
	if projection != wantProjection {
		t.Fatalf("vision selection projection = %q, want %q", projection, wantProjection)
	}
}
