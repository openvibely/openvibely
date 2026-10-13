package sqlite

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestDiscordUserProjectRepo_SetGetDelete(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewDiscordUserProjectRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Discord Project"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	if err := repo.SetUserProject(ctx, "1518288288572641398", project.ID); err != nil {
		t.Fatalf("set user project: %v", err)
	}
	got, err := repo.GetUserProject(ctx, "1518288288572641398")
	if err != nil {
		t.Fatalf("get user project: %v", err)
	}
	if got != project.ID {
		t.Fatalf("project id = %q, want %q", got, project.ID)
	}

	if err := repo.DeleteUserProject(ctx, "1518288288572641398"); err != nil {
		t.Fatalf("delete user project: %v", err)
	}
	got, err = repo.GetUserProject(ctx, "1518288288572641398")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty project after delete, got %q", got)
	}
}

func TestDiscordUserProjectRepo_Upsert(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewDiscordUserProjectRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	p1 := &models.Project{Name: "Discord One"}
	p2 := &models.Project{Name: "Discord Two"}
	if err := projectRepo.Create(ctx, p1); err != nil {
		t.Fatalf("create p1: %v", err)
	}
	if err := projectRepo.Create(ctx, p2); err != nil {
		t.Fatalf("create p2: %v", err)
	}

	if err := repo.SetUserProject(ctx, "1518288288572641398", p1.ID); err != nil {
		t.Fatalf("set p1: %v", err)
	}
	if err := repo.SetUserProject(ctx, "1518288288572641398", p2.ID); err != nil {
		t.Fatalf("set p2: %v", err)
	}
	got, err := repo.GetUserProject(ctx, "1518288288572641398")
	if err != nil {
		t.Fatalf("get user project: %v", err)
	}
	if got != p2.ID {
		t.Fatalf("project id = %q, want %q", got, p2.ID)
	}
}
