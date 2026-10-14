package sqlite

import (
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestAPIChatStatusExactProjection(t *testing.T) {
	db, c := testutil.NewStatementCountingTestDB(t)
	c.SetEnabled(true)
	_, err := NewExecutionRepo(db).GetAPIChatStatusByID(t.Context(), "missing")
	require.NoError(t, err)
	_, err = NewTaskRepo(db, nil).FilterNonChatTaskIDs(t.Context(), []string{"missing"})
	require.NoError(t, err)
	c.SetEnabled(false)
	assertAPIChatStatusCompactStatements(t, c.Statements(), 1)
}
func assertAPIChatStatusCompactStatements(t *testing.T, statements []string, wantTaskQueries int) {
	t.Helper()
	statusQueries := 0
	taskQueries := 0
	for _, statement := range statements {
		normalized := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		if strings.Contains(normalized, "from executions where id = ?") {
			statusQueries++
			projection := strings.Split(normalized, " from executions where id = ?")[0]
			if !strings.Contains(projection, "select id, status") {
				t.Fatalf("API Chat status query does not use compact execution projection: %s", statement)
			}
			for _, forbidden := range []string{"prompt_sent", "reasoning_content", "diff_output"} {
				if strings.Contains(projection, forbidden) {
					t.Fatalf("API Chat status query selected forbidden column %q: %s", forbidden, statement)
				}
			}
		}
		if strings.Contains(normalized, "select id from tasks where id in") && strings.Contains(normalized, "category != ?") {
			taskQueries++
		}
		if strings.Contains(normalized, "from tasks where id = ?") {
			t.Fatalf("API Chat status used per-marker full task lookup: %s", statement)
		}
	}
	if statusQueries != 1 {
		t.Fatalf("API Chat status execution queries = %d, want 1; statements: %#v", statusQueries, statements)
	}
	if taskQueries != wantTaskQueries {
		t.Fatalf("API Chat status task filter queries = %d, want %d; statements: %#v", taskQueries, wantTaskQueries, statements)
	}
}
