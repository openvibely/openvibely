package repository

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/testutil"
)

func TestAttachmentCleanupFilePathQueriesDoNotSort(t *testing.T) {
	db := testutil.NewTestDB(t)

	for name, query := range map[string]string{
		"task attachments": selectAllTaskAttachmentFilePathsSQL,
		"chat attachments": selectAllChatAttachmentFilePathsSQL,
	} {
		t.Run(name, func(t *testing.T) {
			plan := explainAttachmentCleanupQueryPlan(t, db, query)
			if strings.Contains(strings.ToUpper(query), "ORDER BY") {
				t.Fatalf("startup cleanup file path query must not request ordering: %s", query)
			}
			if strings.Contains(plan, "USE TEMP B-TREE FOR ORDER BY") {
				t.Fatalf("startup cleanup file path query must not materialize an ORDER BY sort; plan:\n%s", plan)
			}
		})
	}
}

func explainAttachmentCleanupQueryPlan(tb testing.TB, db *sql.DB, query string, args ...any) string {
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
			tb.Fatalf("scan query plan: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		tb.Fatalf("iterate query plan: %v", err)
	}
	return strings.Join(details, "\n")
}
