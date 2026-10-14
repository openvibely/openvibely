package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/testutil"
)

func TestDiscordAuthorizationMembershipQueryShape(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewDiscordAuthRepo(db)
	counter.Reset()
	counter.SetEnabled(true)
	allowed, err := repo.IsAuthorizedAnywhere(context.Background(), "missing-user")
	counter.SetEnabled(false)
	if err != nil || allowed {
		t.Fatalf("unexpected membership result: %v, %v", allowed, err)
	}
	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("want one membership query, got %q", statements)
	}
	query := " " + strings.Join(strings.Fields(strings.ToLower(statements[0])), " ") + " "
	if !strings.Contains(query, " from discord_authorized_users where ") {
		t.Fatalf("expected authorization membership query, got %q", query)
	}
}
