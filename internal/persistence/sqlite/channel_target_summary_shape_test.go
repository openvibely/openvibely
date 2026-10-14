package sqlite

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestChannelTargetSummaryQueryShape(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewChannelTargetRepo(db)
	ctx := context.Background()
	for i := 0; i < 1200; i++ {
		platform := []string{"email", "slack", "telegram", "discord", "matrix"}[i%5]
		if err := repo.Upsert(ctx, models.ChannelTarget{ID: fmt.Sprintf("summary-%d", i), ProjectID: "default", Platform: platform, TargetID: fmt.Sprintf("%d-%s", i, strings.Repeat("destination", 100)), DefaultSubject: strings.Repeat("subject", 100)}); err != nil {
			t.Fatal(err)
		}
	}
	counter.Reset()
	counter.SetEnabled(true)
	_, err := repo.SummarizeByProject(ctx, "default")
	counter.SetEnabled(false)
	if err != nil {
		t.Fatal(err)
	}
	statements := strings.Join(counter.Statements(), "\n")
	if len(counter.Statements()) != 1 || !strings.Contains(statements, "GROUP BY platform, target_kind") {
		t.Fatalf("expected one grouped aggregate query: %s", statements)
	}
	if strings.Contains(statements, "SELECT id, project_id, platform, target_kind, name, target_id, thread_id, is_home, default_subject, created_at, updated_at") {
		t.Fatalf("summary materialized targets: %s", statements)
	}
	if n := counter.SelectedTextBytes(); n > 2000 {
		t.Fatalf("summary read %d text bytes", n)
	}
}
