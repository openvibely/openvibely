package service

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestLLMServiceSmallHelpers(t *testing.T) {
	if got := statusOrNil(nil); got != "<nil>" {
		t.Fatalf("statusOrNil(nil) = %q", got)
	}
	task := &models.Task{Status: models.StatusRunning}
	if got := statusOrNil(task); got != string(models.StatusRunning) {
		t.Fatalf("statusOrNil(task) = %q", got)
	}

	ctx := WithDirectUsageProject(context.Background(), "project-1")
	if got := directUsageProjectFromContext(ctx); got != "project-1" {
		t.Fatalf("directUsageProjectFromContext = %q", got)
	}
}
