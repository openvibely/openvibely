package sqlite_test

import (
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAtomicAutomationPublicationHasNoLegacyJournal(t *testing.T) {
	db := testutil.NewTestDB(t)
	for _, name := range []string{"automation_publication_attempts", "automation_publication_steps"} {
		var count int
		require.NoError(t, db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&count))
		require.Zero(t, count, name)
	}
}
