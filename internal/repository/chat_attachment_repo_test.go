package repository

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestChatAttachmentRepo_ListByExecutionIDs(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	execRepo := NewExecutionRepo(db)
	attachRepo := NewChatAttachmentRepo(db)
	ctx := context.Background()

	// Create a task and two executions
	task := &models.Task{ProjectID: "default", Title: "Attach Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	taskRepo.Create(ctx, task)

	agentRepo := NewLLMConfigRepo(db)
	agent, _ := agentRepo.GetDefault(ctx)

	exec1 := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "test1"}
	execRepo.Create(ctx, exec1)
	exec2 := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "test2"}
	execRepo.Create(ctx, exec2)
	exec3 := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "test3"}
	execRepo.Create(ctx, exec3)

	// Create attachments for exec1 and exec2, none for exec3
	att1 := &models.ChatAttachment{ExecutionID: exec1.ID, FileName: "file1.png", FilePath: "/tmp/file1.png", MediaType: "image/png", FileSize: 100}
	if err := attachRepo.Create(ctx, att1); err != nil {
		t.Fatalf("Create att1: %v", err)
	}
	att2 := &models.ChatAttachment{ExecutionID: exec1.ID, FileName: "file2.jpg", FilePath: "/tmp/file2.jpg", MediaType: "image/jpeg", FileSize: 200}
	if err := attachRepo.Create(ctx, att2); err != nil {
		t.Fatalf("Create att2: %v", err)
	}
	att3 := &models.ChatAttachment{ExecutionID: exec2.ID, FileName: "file3.pdf", FilePath: "/tmp/file3.pdf", MediaType: "application/pdf", FileSize: 300}
	if err := attachRepo.Create(ctx, att3); err != nil {
		t.Fatalf("Create att3: %v", err)
	}

	// Batch query all three execution IDs
	result, err := attachRepo.ListByExecutionIDs(ctx, []string{exec1.ID, exec2.ID, exec3.ID})
	if err != nil {
		t.Fatalf("ListByExecutionIDs: %v", err)
	}

	// exec1 should have 2 attachments
	if len(result[exec1.ID]) != 2 {
		t.Errorf("expected 2 attachments for exec1, got %d", len(result[exec1.ID]))
	}
	// exec2 should have 1 attachment
	if len(result[exec2.ID]) != 1 {
		t.Errorf("expected 1 attachment for exec2, got %d", len(result[exec2.ID]))
	}
	// exec3 should have 0 attachments (not present in map)
	if len(result[exec3.ID]) != 0 {
		t.Errorf("expected 0 attachments for exec3, got %d", len(result[exec3.ID]))
	}

	// Verify attachment data
	if result[exec1.ID][0].FileName != "file1.png" {
		t.Errorf("expected file1.png, got %s", result[exec1.ID][0].FileName)
	}
	if result[exec2.ID][0].FileName != "file3.pdf" {
		t.Errorf("expected file3.pdf, got %s", result[exec2.ID][0].FileName)
	}
}

func TestChatAttachmentRepo_ListByExecutionIDs_Empty(t *testing.T) {
	db := testutil.NewTestDB(t)
	attachRepo := NewChatAttachmentRepo(db)
	ctx := context.Background()

	result, err := attachRepo.ListByExecutionIDs(ctx, []string{})
	if err != nil {
		t.Fatalf("ListByExecutionIDs empty: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty map, got %d entries", len(result))
	}
}

func TestChatAttachmentRepo_CleanupOrphanedFiles_RelativeRootPreservesTrackedFiles(t *testing.T) {
	db := testutil.NewTestDB(t)
	chatAttachmentRepo := NewChatAttachmentRepo(db)
	attachmentRepo := NewAttachmentRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	execRepo := NewExecutionRepo(db)
	agentRepo := NewLLMConfigRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Chat cleanup",
		Category:  models.CategoryChat,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	agent, err := agentRepo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("Create execution: %v", err)
	}

	tmpDir := t.TempDir()
	uploadsDirAbs := filepath.Join(tmpDir, "uploads")
	chatDir := filepath.Join(uploadsDirAbs, "chat", exec.ID)
	taskDir := filepath.Join(uploadsDirAbs, task.ID)
	if err := os.MkdirAll(chatDir, 0755); err != nil {
		t.Fatalf("mkdir chat dir: %v", err)
	}
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		t.Fatalf("mkdir task dir: %v", err)
	}

	trackedChatFile := filepath.Join(chatDir, "tracked.png")
	if err := os.WriteFile(trackedChatFile, []byte("png"), 0644); err != nil {
		t.Fatalf("write tracked chat file: %v", err)
	}
	if err := chatAttachmentRepo.Create(ctx, &models.ChatAttachment{
		ExecutionID: exec.ID,
		FileName:    "tracked.png",
		FilePath:    trackedChatFile,
		MediaType:   "image/png",
		FileSize:    3,
	}); err != nil {
		t.Fatalf("Create tracked chat attachment: %v", err)
	}

	orphanedChatFile := filepath.Join(chatDir, "orphaned.png")
	if err := os.WriteFile(orphanedChatFile, []byte("orphaned"), 0644); err != nil {
		t.Fatalf("write orphaned chat file: %v", err)
	}

	trackedTaskFile := filepath.Join(taskDir, "task.txt")
	if err := os.WriteFile(trackedTaskFile, []byte("task"), 0644); err != nil {
		t.Fatalf("write tracked task file: %v", err)
	}
	if err := attachmentRepo.Create(ctx, &models.Attachment{
		TaskID:    task.ID,
		FileName:  "task.txt",
		FilePath:  trackedTaskFile,
		MediaType: "text/plain",
		FileSize:  4,
	}); err != nil {
		t.Fatalf("Create tracked task attachment: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	relativeUploadsDir, err := filepath.Rel(cwd, uploadsDirAbs)
	if err != nil {
		t.Fatalf("filepath.Rel: %v", err)
	}

	count, err := chatAttachmentRepo.CleanupOrphanedFiles(ctx, relativeUploadsDir)
	if err != nil {
		t.Fatalf("CleanupOrphanedFiles: %v", err)
	}

	if count != 1 {
		t.Fatalf("expected 1 orphaned chat file deleted, got %d", count)
	}
	if _, err := os.Stat(trackedChatFile); os.IsNotExist(err) {
		t.Fatal("tracked chat file should still exist after cleanup")
	}
	if _, err := os.Stat(orphanedChatFile); !os.IsNotExist(err) {
		t.Fatal("orphaned chat file should be deleted after cleanup")
	}
	if _, err := os.Stat(trackedTaskFile); os.IsNotExist(err) {
		t.Fatal("task attachment should not be touched by chat cleanup")
	}
}
