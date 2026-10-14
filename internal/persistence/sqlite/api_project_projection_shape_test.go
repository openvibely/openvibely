package sqlite_test

import (
	"context"
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil"
	"strings"
	"testing"
)

func TestAPIProjectProjectionShape(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	counter.Reset()
	counter.SetEnabled(true)
	_, err := sqlite.NewProjectRepo(db).ListAPIProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("APIGetProjects statements = %d, want 1: %#v", len(statements), statements)
	}
	query := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
	if query != "select id, name, repo_path, created_at from projects order by is_default desc, name asc" {
		t.Fatalf("APIGetProjects query = %q", query)
	}
	for _, omitted := range []string{"description", "repo_url", "default_agent_config_id", "max_workers", "updated_at"} {
		if strings.Contains(query, omitted) {
			t.Errorf("APIGetProjects selected omitted column %q: %s", omitted, query)
		}
	}

}
