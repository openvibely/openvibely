package sqlite

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/pkg/attachmentcontext"
)

func TestAttachmentRepo_ExecutionScopedAttachmentsInvalidateContext(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	repo := NewAttachmentRepo(db)
	project := &models.Project{Name: "Exec attachments"}
	if err := NewProjectRepo(db).Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Thread", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	if err := NewTaskRepo(db, nil).Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	exec := &models.Execution{TaskID: task.ID, Status: models.ExecCompleted}
	if err := NewExecutionRepo(db).Create(ctx, exec); err != nil {
		t.Fatal(err)
	}

	loads := 0
	source := attachmentcontext.NewSource(func(ctx context.Context) (string, error) {
		loads++
		atts, err := repo.ListByTask(ctx, task.ID)
		if err != nil {
			return "", err
		}
		refs := ""
		for _, a := range atts {
			refs += a.FileName + "\n"
		}
		return refs, nil
	}, task.ID)
	if err := source.Load(ctx); err != nil {
		t.Fatal(err)
	}

	panel := &models.Attachment{TaskID: task.ID, FileName: "spec.pdf", FilePath: "/tmp/spec.pdf", MediaType: "application/pdf", FileSize: 1}
	msg := &models.Attachment{TaskID: task.ID, ExecutionID: exec.ID, FileName: "shot.png", FilePath: "/tmp/shot.png", MediaType: "image/png", FileSize: 1}
	for _, a := range []*models.Attachment{panel, msg} {
		if err := repo.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if refs := source.References(ctx); refs != "spec.pdf\nshot.png\n" || loads != 2 {
		t.Fatalf("refs=%q loads=%d", refs, loads)
	}
	_ = source.References(ctx)
	if loads != 2 {
		t.Fatal("source reloaded without changes")
	}

	got, err := repo.GetByID(ctx, msg.ID)
	if err != nil || got.ExecutionID != exec.ID {
		t.Fatalf("execution id not persisted: %+v %v", got, err)
	}
	byExec, err := repo.ListByExecutionIDs(ctx, []string{exec.ID, "missing"})
	if err != nil || len(byExec[exec.ID]) != 1 || byExec[exec.ID][0].ID != msg.ID {
		t.Fatalf("ListByExecutionIDs = %+v %v", byExec, err)
	}

	if err := repo.Delete(ctx, panel.ID); err != nil {
		t.Fatal(err)
	}
	if refs := source.References(ctx); refs != "shot.png\n" {
		t.Fatalf("delete not reflected: %q", refs)
	}
	if err := repo.DeleteByIDForProject(ctx, msg.ID, project.ID); err != nil {
		t.Fatal(err)
	}
	if refs := source.References(ctx); refs != "" {
		t.Fatalf("project delete not reflected: %q", refs)
	}
}
