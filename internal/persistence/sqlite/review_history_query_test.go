package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/testutil"
)

func TestReviewHistoryReadUsesBoundedNewestFirstQuery(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewExecutionRepo(db)
	counter.Reset()
	counter.SetEnabled(true)
	_, err := repo.ListByTaskChronologicalLimit(context.Background(), "missing-task", 20)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatal(err)
	}
	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("history statements: %v", statements)
	}
	query := statements[0]
	if !strings.Contains(query, "ORDER BY started_at DESC, rowid DESC LIMIT ?") || strings.Contains(query, "ORDER BY started_at ASC, rowid ASC") {
		t.Fatalf("unbounded or incorrectly ordered history: %s", query)
	}
}
