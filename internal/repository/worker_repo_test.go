package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/testutil"
)

func TestWorkerRepo_PersistsHighGlobalLimitAndRejectsNegative(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewWorkerRepo(db)
	ctx := context.Background()

	if err := repo.SetMaxWorkers(ctx, 25); err != nil {
		t.Fatalf("SetMaxWorkers(25): %v", err)
	}
	got, err := repo.GetMaxWorkers(ctx)
	if err != nil {
		t.Fatalf("GetMaxWorkers: %v", err)
	}
	if got != 25 {
		t.Fatalf("GetMaxWorkers = %d, want 25", got)
	}

	if err := repo.SetMaxWorkers(ctx, -1); err == nil || !strings.Contains(err.Error(), "non-negative") {
		t.Fatalf("SetMaxWorkers(-1) error = %v, want non-negative validation", err)
	}
	got, err = repo.GetMaxWorkers(ctx)
	if err != nil {
		t.Fatalf("GetMaxWorkers after rejected update: %v", err)
	}
	if got != 25 {
		t.Fatalf("rejected negative update changed max_workers to %d", got)
	}
}
