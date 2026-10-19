package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/openvibely/openvibely/pkg/attachmentcontext"
)

func TestExecuteTaskWithAgent_TaskWideAttachmentContextRefreshesDuringRun(t *testing.T) {
	ctx := context.Background()
	db := teststores.New(t)
	teststores.ClearLLMConfigs(t, db.LLMConfigs)
	agent := &models.LLMConfig{Name: "Anthropic", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodAPIKey, APIKey: "k", Model: "claude-sonnet-4-5-20250929", MaxTokens: 4096}
	if err := db.LLMConfigs.Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: "default", Title: "Context task", Prompt: "Read the files", Category: models.CategoryBacklog, Status: models.StatusPending}
	if err := db.Tasks.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	prior := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted}
	if err := db.Executions.Create(ctx, prior); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	panel := &models.Attachment{TaskID: task.ID, FileName: "spec.pdf", FilePath: write("spec.pdf"), MediaType: "application/pdf", FileSize: 1}
	message := &models.Attachment{TaskID: task.ID, ExecutionID: prior.ID, FileName: "shot.png", FilePath: write("shot.png"), MediaType: "image/png", FileSize: 1}
	for _, a := range []*models.Attachment{panel, message} {
		if err := db.Attachments.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	added := &models.Attachment{TaskID: task.ID, FileName: "late.txt", FilePath: write("late.txt"), MediaType: "text/plain", FileSize: 1}
	var before, after string
	var direct []models.Attachment
	svc := NewLLMService(db.LLMConfigs, db.Executions, db.Tasks, nil, nil, db.Attachments)
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{models.ProviderAnthropic: providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		before = attachmentcontext.Text(req.Ctx)
		direct = req.Attachments
		if err := db.Attachments.Create(context.Background(), added); err != nil {
			t.Fatal(err)
		}
		after = attachmentcontext.Text(req.Ctx)
		return llmcontracts.AgentResult{Output: "done"}, nil
	})}
	if _, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(before, "spec.pdf") || !strings.Contains(before, "shot.png") || strings.Contains(before, "[message ") || strings.Contains(before, "[task]") {
		t.Fatalf("initial context missing task attachments:\n%s", before)
	}
	if strings.Contains(before, "late.txt") || !strings.Contains(after, "late.txt") {
		t.Fatalf("context not refreshed after add:\nbefore=%s\nafter=%s", before, after)
	}
	if len(direct) != 1 || direct[0].ID != panel.ID {
		t.Fatalf("only task-level files are direct inputs; message files stay in their original message: %+v", direct)
	}
}

func TestPublishTaskSteeringAttachmentsKeepsUploadsUntilCommitted(t *testing.T) {
	ctx := context.Background()
	db := teststores.New(t)
	task := &models.Task{ProjectID: "default", Title: "Steer", Prompt: "p", Category: models.CategoryActive, Status: models.StatusRunning}
	if err := db.Tasks.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	exec := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	if err := db.Executions.Create(ctx, exec); err != nil {
		t.Fatal(err)
	}
	svc := NewLLMService(db.LLMConfigs, db.Executions, db.Tasks, nil, nil, db.Attachments)
	svc.taskSvc = &TaskService{uploadsDir: t.TempDir()}
	pending := filepath.Join(svc.taskSvc.uploadsDir, "chat", "pending", "s1")
	if err := os.MkdirAll(pending, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pending, "shot.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	inputs := []models.ThreadInput{{AttachmentSessionID: "s1"}}
	countRows := func() int {
		rows, err := db.Attachments.ListByTask(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		return len(rows)
	}

	finish, err := svc.publishTaskSteeringAttachments(ctx, task.ID, exec.ID, inputs)
	if err != nil {
		t.Fatal(err)
	}
	finish(false)
	if countRows() != 0 {
		t.Fatal("failed commit must remove published rows")
	}
	if _, err := os.Stat(pending); err != nil {
		t.Fatalf("failed commit must keep pending uploads for retry: %v", err)
	}

	finish, err = svc.publishTaskSteeringAttachments(ctx, task.ID, exec.ID, inputs)
	if err != nil {
		t.Fatalf("retry must republish from pending uploads: %v", err)
	}
	finish(true)
	if countRows() != 1 {
		t.Fatal("committed retry must keep exactly one row")
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatalf("committed publish must remove pending uploads: %v", err)
	}
}
