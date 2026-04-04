package repository

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestAttachmentRepo_Create(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewAttachmentRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)

	// Create a project first
	project := &models.Project{
		Name: "Test Project",
	}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create a task
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Test Task",
		Category:  models.CategoryActive,
		Priority:  1,
		Status:    models.StatusPending,
		Prompt:    "Do something",
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	att := &models.Attachment{
		TaskID:    task.ID,
		FileName:  "screenshot.png",
		FilePath:  "/tmp/screenshot.png",
		MediaType: "image/png",
		FileSize:  1024,
	}

	err := repo.Create(context.Background(), att)
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	if att.ID == "" {
		t.Error("expected ID to be set")
	}
	if att.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set")
	}
}

func TestAttachmentRepo_GetByID(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewAttachmentRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)

	// Create a project first
	project := &models.Project{
		Name: "Test Project",
	}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create a task
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Test Task",
		Category:  models.CategoryActive,
		Priority:  1,
		Status:    models.StatusPending,
		Prompt:    "Do something",
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	att := &models.Attachment{
		TaskID:    task.ID,
		FileName:  "screenshot.png",
		FilePath:  "/tmp/screenshot.png",
		MediaType: "image/png",
		FileSize:  1024,
	}
	if err := repo.Create(context.Background(), att); err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	retrieved, err := repo.GetByID(context.Background(), att.ID)
	if err != nil {
		t.Fatalf("GetByID() error: %v", err)
	}

	if retrieved == nil {
		t.Fatal("expected attachment to be found")
	}
	if retrieved.ID != att.ID {
		t.Errorf("expected ID %s, got %s", att.ID, retrieved.ID)
	}
	if retrieved.FileName != att.FileName {
		t.Errorf("expected FileName %s, got %s", att.FileName, retrieved.FileName)
	}
}

func TestAttachmentRepo_ListByTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewAttachmentRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)

	// Create a project first
	project := &models.Project{
		Name: "Test Project",
	}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create a task
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Test Task",
		Category:  models.CategoryActive,
		Priority:  1,
		Status:    models.StatusPending,
		Prompt:    "Do something",
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Create multiple attachments
	att1 := &models.Attachment{
		TaskID:    task.ID,
		FileName:  "screenshot1.png",
		FilePath:  "/tmp/screenshot1.png",
		MediaType: "image/png",
		FileSize:  1024,
	}
	att2 := &models.Attachment{
		TaskID:    task.ID,
		FileName:  "screenshot2.jpg",
		FilePath:  "/tmp/screenshot2.jpg",
		MediaType: "image/jpeg",
		FileSize:  2048,
	}

	if err := repo.Create(context.Background(), att1); err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if err := repo.Create(context.Background(), att2); err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	attachments, err := repo.ListByTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListByTask() error: %v", err)
	}

	if len(attachments) != 2 {
		t.Errorf("expected 2 attachments, got %d", len(attachments))
	}
}

func TestAttachmentRepo_Delete(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewAttachmentRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)

	// Create a project first
	project := &models.Project{
		Name: "Test Project",
	}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create a task
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Test Task",
		Category:  models.CategoryActive,
		Priority:  1,
		Status:    models.StatusPending,
		Prompt:    "Do something",
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	att := &models.Attachment{
		TaskID:    task.ID,
		FileName:  "screenshot.png",
		FilePath:  "/tmp/screenshot.png",
		MediaType: "image/png",
		FileSize:  1024,
	}
	if err := repo.Create(context.Background(), att); err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	err := repo.Delete(context.Background(), att.ID)
	if err != nil {
		t.Fatalf("Delete() error: %v", err)
	}

	retrieved, err := repo.GetByID(context.Background(), att.ID)
	if err != nil {
		t.Fatalf("GetByID() error: %v", err)
	}
	if retrieved != nil {
		t.Error("expected attachment to be deleted")
	}
}

func TestAttachmentRepo_DeleteByTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewAttachmentRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)

	// Create a project first
	project := &models.Project{
		Name: "Test Project",
	}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create a task
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Test Task",
		Category:  models.CategoryActive,
		Priority:  1,
		Status:    models.StatusPending,
		Prompt:    "Do something",
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Create multiple attachments
	att1 := &models.Attachment{
		TaskID:    task.ID,
		FileName:  "screenshot1.png",
		FilePath:  "/tmp/screenshot1.png",
		MediaType: "image/png",
		FileSize:  1024,
	}
	att2 := &models.Attachment{
		TaskID:    task.ID,
		FileName:  "screenshot2.jpg",
		FilePath:  "/tmp/screenshot2.jpg",
		MediaType: "image/jpeg",
		FileSize:  2048,
	}

	if err := repo.Create(context.Background(), att1); err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if err := repo.Create(context.Background(), att2); err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	err := repo.DeleteByTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("DeleteByTask() error: %v", err)
	}

	attachments, err := repo.ListByTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListByTask() error: %v", err)
	}
	if len(attachments) != 0 {
		t.Errorf("expected 0 attachments, got %d", len(attachments))
	}
}

func TestAttachmentRepo_CleanupOrphanedFiles(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewAttachmentRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)

	// Create a temporary uploads directory for testing
	tmpDir := t.TempDir()
	uploadsDir := filepath.Join(tmpDir, "uploads")
	if err := os.MkdirAll(uploadsDir, 0755); err != nil {
		t.Fatalf("failed to create uploads directory: %v", err)
	}

	// Create a project
	project := &models.Project{
		Name: "Test Project",
	}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create a task
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Test Task",
		Category:  models.CategoryActive,
		Priority:  1,
		Status:    models.StatusPending,
		Prompt:    "Do something",
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Create task directory
	taskDir := filepath.Join(uploadsDir, task.ID)
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		t.Fatalf("failed to create task directory: %v", err)
	}

	// Create a tracked file (in DB)
	trackedFile := filepath.Join(taskDir, "tracked.txt")
	if err := os.WriteFile(trackedFile, []byte("tracked"), 0644); err != nil {
		t.Fatalf("failed to create tracked file: %v", err)
	}
	trackedAtt := &models.Attachment{
		TaskID:    task.ID,
		FileName:  "tracked.txt",
		FilePath:  trackedFile,
		MediaType: "text/plain",
		FileSize:  7,
	}
	if err := repo.Create(context.Background(), trackedAtt); err != nil {
		t.Fatalf("failed to create tracked attachment: %v", err)
	}

	// Create an orphaned file (not in DB)
	orphanedFile := filepath.Join(taskDir, "orphaned.txt")
	if err := os.WriteFile(orphanedFile, []byte("orphaned"), 0644); err != nil {
		t.Fatalf("failed to create orphaned file: %v", err)
	}

	// Create a second task with only orphaned files
	task2 := &models.Task{
		ProjectID: project.ID,
		Title:     "Test Task 2",
		Category:  models.CategoryActive,
		Priority:  1,
		Status:    models.StatusPending,
		Prompt:    "Do something",
	}
	if err := taskRepo.Create(context.Background(), task2); err != nil {
		t.Fatalf("failed to create task 2: %v", err)
	}
	task2Dir := filepath.Join(uploadsDir, task2.ID)
	if err := os.MkdirAll(task2Dir, 0755); err != nil {
		t.Fatalf("failed to create task2 directory: %v", err)
	}
	orphanedFile2 := filepath.Join(task2Dir, "orphaned2.txt")
	if err := os.WriteFile(orphanedFile2, []byte("orphaned2"), 0644); err != nil {
		t.Fatalf("failed to create orphaned file 2: %v", err)
	}

	// Verify files exist before cleanup
	if _, err := os.Stat(trackedFile); os.IsNotExist(err) {
		t.Fatal("tracked file should exist before cleanup")
	}
	if _, err := os.Stat(orphanedFile); os.IsNotExist(err) {
		t.Fatal("orphaned file should exist before cleanup")
	}
	if _, err := os.Stat(orphanedFile2); os.IsNotExist(err) {
		t.Fatal("orphaned file 2 should exist before cleanup")
	}

	// Run cleanup
	count, err := repo.CleanupOrphanedFiles(context.Background(), uploadsDir)
	if err != nil {
		t.Fatalf("CleanupOrphanedFiles() error: %v", err)
	}

	if count != 2 {
		t.Errorf("expected 2 orphaned files deleted, got %d", count)
	}

	// Verify tracked file still exists
	if _, err := os.Stat(trackedFile); os.IsNotExist(err) {
		t.Error("tracked file should still exist after cleanup")
	}

	// Verify orphaned file is deleted
	if _, err := os.Stat(orphanedFile); !os.IsNotExist(err) {
		t.Error("orphaned file should be deleted after cleanup")
	}

	// Verify orphaned file 2 is deleted
	if _, err := os.Stat(orphanedFile2); !os.IsNotExist(err) {
		t.Error("orphaned file 2 should be deleted after cleanup")
	}

	// Verify task2 directory is removed (it was empty after cleanup)
	if _, err := os.Stat(task2Dir); !os.IsNotExist(err) {
		t.Error("empty task2 directory should be removed after cleanup")
	}

	// Verify task directory still exists (it has tracked file)
	if _, err := os.Stat(taskDir); os.IsNotExist(err) {
		t.Error("task directory with tracked files should still exist")
	}
}

func TestAttachmentRepo_CleanupOrphanedFiles_NoUploadsDir(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewAttachmentRepo(db)

	// Use a non-existent directory
	tmpDir := t.TempDir()
	uploadsDir := filepath.Join(tmpDir, "nonexistent")

	// Should not error when uploads directory doesn't exist
	count, err := repo.CleanupOrphanedFiles(context.Background(), uploadsDir)
	if err != nil {
		t.Fatalf("CleanupOrphanedFiles() should not error on nonexistent dir: %v", err)
	}

	if count != 0 {
		t.Errorf("expected 0 files deleted, got %d", count)
	}
}

func TestAttachmentRepo_CleanupOrphanedFiles_EmptyDatabase(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewAttachmentRepo(db)

	// Create a temporary uploads directory with files
	tmpDir := t.TempDir()
	uploadsDir := filepath.Join(tmpDir, "uploads")
	if err := os.MkdirAll(uploadsDir, 0755); err != nil {
		t.Fatalf("failed to create uploads directory: %v", err)
	}

	taskDir := filepath.Join(uploadsDir, "some-task-id")
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		t.Fatalf("failed to create task directory: %v", err)
	}

	orphanedFile := filepath.Join(taskDir, "orphaned.txt")
	if err := os.WriteFile(orphanedFile, []byte("orphaned"), 0644); err != nil {
		t.Fatalf("failed to create orphaned file: %v", err)
	}

	// Run cleanup with empty database (all files are orphaned)
	count, err := repo.CleanupOrphanedFiles(context.Background(), uploadsDir)
	if err != nil {
		t.Fatalf("CleanupOrphanedFiles() error: %v", err)
	}

	if count != 1 {
		t.Errorf("expected 1 orphaned file deleted, got %d", count)
	}

	// Verify file is deleted
	if _, err := os.Stat(orphanedFile); !os.IsNotExist(err) {
		t.Error("orphaned file should be deleted")
	}

	// Verify empty directory is removed
	if _, err := os.Stat(taskDir); !os.IsNotExist(err) {
		t.Error("empty task directory should be removed")
	}
}

func TestAttachmentRepo_CleanupOrphanedFiles_RelativeRootSkipsChatAndPreservesTrackedFiles(t *testing.T) {
	db := testutil.NewTestDB(t)
	attachmentRepo := NewAttachmentRepo(db)
	chatAttachmentRepo := NewChatAttachmentRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	execRepo := NewExecutionRepo(db)
	agentRepo := NewLLMConfigRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Cleanup Project"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Task with attachment",
		Category:  models.CategoryActive,
		Priority:  1,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := agentRepo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	chatTask := &models.Task{
		ProjectID: project.ID,
		Title:     "Chat task",
		Category:  models.CategoryChat,
		Priority:  1,
		Status:    models.StatusPending,
		Prompt:    "chat",
		AgentID:   &agent.ID,
	}
	if err := taskRepo.Create(ctx, chatTask); err != nil {
		t.Fatalf("failed to create chat task: %v", err)
	}

	exec := &models.Execution{
		TaskID:        chatTask.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "chat",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	tmpDir := t.TempDir()
	uploadsDirAbs := filepath.Join(tmpDir, "uploads")
	taskDir := filepath.Join(uploadsDirAbs, "tasks", task.ID)
	chatDir := filepath.Join(uploadsDirAbs, "chat", exec.ID)
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		t.Fatalf("failed to create task dir: %v", err)
	}
	if err := os.MkdirAll(chatDir, 0755); err != nil {
		t.Fatalf("failed to create chat dir: %v", err)
	}

	trackedTaskFile := filepath.Join(taskDir, "tracked.txt")
	if err := os.WriteFile(trackedTaskFile, []byte("tracked"), 0644); err != nil {
		t.Fatalf("failed to create tracked task file: %v", err)
	}
	if err := attachmentRepo.Create(ctx, &models.Attachment{
		TaskID:    task.ID,
		FileName:  "tracked.txt",
		FilePath:  trackedTaskFile,
		MediaType: "text/plain",
		FileSize:  7,
	}); err != nil {
		t.Fatalf("failed to create tracked task attachment: %v", err)
	}

	orphanedTaskFile := filepath.Join(taskDir, "orphaned.txt")
	if err := os.WriteFile(orphanedTaskFile, []byte("orphaned"), 0644); err != nil {
		t.Fatalf("failed to create orphaned task file: %v", err)
	}

	trackedChatFile := filepath.Join(chatDir, "screenshot.png")
	if err := os.WriteFile(trackedChatFile, []byte("png"), 0644); err != nil {
		t.Fatalf("failed to create tracked chat file: %v", err)
	}
	if err := chatAttachmentRepo.Create(ctx, &models.ChatAttachment{
		ExecutionID: exec.ID,
		FileName:    "screenshot.png",
		FilePath:    trackedChatFile,
		MediaType:   "image/png",
		FileSize:    3,
	}); err != nil {
		t.Fatalf("failed to create tracked chat attachment: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	relativeUploadsDir, err := filepath.Rel(cwd, uploadsDirAbs)
	if err != nil {
		t.Fatalf("failed to build relative uploads path: %v", err)
	}

	count, err := attachmentRepo.CleanupOrphanedFiles(ctx, relativeUploadsDir)
	if err != nil {
		t.Fatalf("CleanupOrphanedFiles() error: %v", err)
	}

	if count != 1 {
		t.Fatalf("expected 1 orphaned task file deleted, got %d", count)
	}
	if _, err := os.Stat(trackedTaskFile); os.IsNotExist(err) {
		t.Fatal("tracked task file should still exist after cleanup")
	}
	if _, err := os.Stat(orphanedTaskFile); !os.IsNotExist(err) {
		t.Fatal("orphaned task file should be deleted after cleanup")
	}
	if _, err := os.Stat(trackedChatFile); os.IsNotExist(err) {
		t.Fatal("chat attachment should not be touched by task attachment cleanup")
	}
}
