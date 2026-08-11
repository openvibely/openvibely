package service

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestCompleteChannelExecutionPublishesFailedTerminalEvent(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	_, task, exec := createChannelExecutionStreamFixture(t, ctx, db, execRepo, taskRepo)
	hub := events.NewExecutionStreamHub()
	sub, _, err := hub.Subscribe(exec.ID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	completeChannelExecution(ctx, channelExecutionCompletionOptions{
		Platform:           "test",
		ExecRepo:           execRepo,
		TaskRepo:           taskRepo,
		ExecutionStreamHub: hub,
		ExecID:             exec.ID,
		TaskID:             task.ID,
		ErrorMessage:       "channel provider failed",
		DurationMs:         1,
	})

	select {
	case event, ok := <-sub:
		if !ok || event.ExecID != exec.ID || event.Type != events.ExecutionStreamError || event.Error != "channel provider failed" {
			t.Fatalf("terminal event = %+v, open=%v", event, ok)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for failed channel terminal event")
	}
	if _, ok := <-sub; ok {
		t.Fatal("subscriber remained open after failed channel terminal event")
	}
	if got := hub.SubscriberCount(); got != 0 {
		t.Fatalf("subscriber count after failed terminal event = %d", got)
	}
}

func createChannelExecutionStreamFixture(t *testing.T, ctx context.Context, db *sql.DB, execRepo *repository.ExecutionRepo, taskRepo *repository.TaskRepo) (*models.Project, *models.Task, *models.Execution) {
	t.Helper()
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	agent := ensureDefaultAgent(t, llmConfigRepo)
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Channel execution stream project", RepoPath: t.TempDir()}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Channel execution stream task", Category: models.CategoryChat, Status: models.StatusRunning, Prompt: "channel prompt", AgentID: &agent.ID}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: task.Prompt}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}
	return project, task, exec
}

func TestCompleteChannelExecutionDoesNotPublishTerminalWhenExecutionCompleteFails(t *testing.T) {
	db := testutil.NewTestDB(t)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	hub := events.NewExecutionStreamHub()
	sub, unsubscribe, err := hub.Subscribe("exec-channel-failed-complete")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()

	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	completeChannelExecution(context.Background(), channelExecutionCompletionOptions{
		Platform:           "test",
		ExecRepo:           execRepo,
		TaskRepo:           taskRepo,
		ExecutionStreamHub: hub,
		ExecID:             "exec-channel-failed-complete",
		TaskID:             "task-channel-failed-complete",
		Output:             "should not complete",
		DurationMs:         1,
	})

	select {
	case event := <-sub:
		t.Fatalf("unexpected terminal event after failed durable complete: %#v", event)
	case <-time.After(25 * time.Millisecond):
	}
}
