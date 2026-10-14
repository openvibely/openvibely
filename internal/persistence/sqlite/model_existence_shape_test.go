package sqlite_test

import (
	"context"
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil"
	"strings"
	"testing"
)

func TestModelExistenceProjectionShape(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	counter.Reset()
	counter.SetEnabled(true)
	_, err := sqlite.NewLLMConfigRepo(db).HasAny(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("statements = %#v, want exactly one model availability query", statements)
	}
	stmt := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
	if stmt != "select exists(select 1 from agent_configs)" {
		t.Fatalf("hasConfiguredModels query = %q, want SELECT EXISTS", statements[0])
	}
	for _, forbidden := range []string{"api_key", "oauth_access_token", "oauth_refresh_token", "oauth_client_secret", "extra_body_json", "custom_auth_state_json", "order by"} {
		if strings.Contains(stmt, forbidden) {
			t.Fatalf("hasConfiguredModels query selected forbidden data %q: %s", forbidden, statements[0])
		}
	}
}
