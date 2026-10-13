package sqlite

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestEmailSenderProjectRepo_SetGetDelete(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewEmailSenderProjectRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Email Project"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	if err := repo.SetSenderProject(ctx, "james@example.com", project.ID); err != nil {
		t.Fatalf("set sender project: %v", err)
	}

	got, err := repo.GetSenderProject(ctx, "james@example.com")
	if err != nil {
		t.Fatalf("get sender project: %v", err)
	}
	if got != project.ID {
		t.Fatalf("project id = %q, want %q", got, project.ID)
	}

	// Normalize: upper-case input should resolve to same row.
	got2, err := repo.GetSenderProject(ctx, "JAMES@EXAMPLE.COM")
	if err != nil {
		t.Fatalf("get normalized: %v", err)
	}
	if got2 != project.ID {
		t.Fatalf("normalized project id = %q, want %q", got2, project.ID)
	}

	if err := repo.DeleteSenderProject(ctx, "james@example.com"); err != nil {
		t.Fatalf("delete sender project: %v", err)
	}
	got, err = repo.GetSenderProject(ctx, "james@example.com")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty after delete, got %q", got)
	}
}

func TestEmailSenderProjectRepo_Upsert(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewEmailSenderProjectRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	p1 := &models.Project{Name: "Email One"}
	p2 := &models.Project{Name: "Email Two"}
	if err := projectRepo.Create(ctx, p1); err != nil {
		t.Fatalf("create p1: %v", err)
	}
	if err := projectRepo.Create(ctx, p2); err != nil {
		t.Fatalf("create p2: %v", err)
	}

	if err := repo.SetSenderProject(ctx, "james@example.com", p1.ID); err != nil {
		t.Fatalf("set p1: %v", err)
	}
	if err := repo.SetSenderProject(ctx, "james@example.com", p2.ID); err != nil {
		t.Fatalf("set p2: %v", err)
	}
	got, err := repo.GetSenderProject(ctx, "james@example.com")
	if err != nil {
		t.Fatalf("get sender project: %v", err)
	}
	if got != p2.ID {
		t.Fatalf("project id = %q, want %q", got, p2.ID)
	}
}

func TestEmailSenderProjectRepo_GetMissing(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewEmailSenderProjectRepo(db)
	ctx := context.Background()

	got, err := repo.GetSenderProject(ctx, "nobody@example.com")
	if err != nil {
		t.Fatalf("get missing: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty for missing, got %q", got)
	}
}
