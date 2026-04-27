package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestViewHistoryRendersExecutionWithNullAgentConfigID(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("History Project").Build()
	agent := tc.CreateLLMConfig().WithName("Deleted Model").Build()
	task := tc.CreateTask(project.ID).
		WithTitle("Historical Task").
		WithCategory(models.CategoryCompleted).
		WithStatus(models.StatusCompleted).
		Build()
	exec := tc.CreateExecution(task.ID, agent.ID).
		WithPromptSent("Run history task").
		Build()
	if err := tc.execRepo.Complete(context.Background(), exec.ID, models.ExecCompleted, "History output", "", 0, 1000); err != nil {
		t.Fatalf("complete execution: %v", err)
	}
	if err := tc.llmConfigRepo.Delete(context.Background(), agent.ID); err != nil {
		t.Fatalf("delete model config: %v", err)
	}

	rec := tc.HTTP().Get("/history?project_id=" + project.ID).Execute()
	tc.Assert(rec).
		StatusCode(http.StatusOK).
		Contains("Historical Task").
		Contains("No model config")
}
