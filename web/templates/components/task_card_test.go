package components

import (
	"bytes"
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestStatusLabelForTask(t *testing.T) {
	tests := []struct {
		name     string
		status   models.TaskStatus
		category models.TaskCategory
		want     string
	}{
		{"active pending shows Queued", models.StatusPending, models.CategoryActive, "Queued"},
		{"backlog pending shows Pending", models.StatusPending, models.CategoryBacklog, "Pending"},
		{"scheduled pending shows Scheduled", models.StatusPending, models.CategoryScheduled, "Scheduled"},
		{"completed pending shows Queued", models.StatusPending, models.CategoryCompleted, "Queued"},
		{"active running shows In Progress", models.StatusRunning, models.CategoryActive, "In Progress"},
		{"backlog running shows In Progress", models.StatusRunning, models.CategoryBacklog, "In Progress"},
		{"any completed shows Completed", models.StatusCompleted, models.CategoryActive, "Completed"},
		{"any failed shows Failed", models.StatusFailed, models.CategoryBacklog, "Failed"},
		{"any cancelled shows Cancelled", models.StatusCancelled, models.CategoryActive, "Cancelled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StatusLabelForTask(tt.status, tt.category)
			if got != tt.want {
				t.Errorf("StatusLabelForTask(%q, %q) = %q, want %q", tt.status, tt.category, got, tt.want)
			}
		})
	}
}

func TestStatusLabel(t *testing.T) {
	tests := []struct {
		status models.TaskStatus
		want   string
	}{
		{models.StatusPending, "Queued"},
		{models.StatusRunning, "In Progress"},
		{models.StatusCompleted, "Completed"},
		{models.StatusFailed, "Failed"},
		{models.StatusCancelled, "Cancelled"},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			got := StatusLabel(tt.status)
			if got != tt.want {
				t.Errorf("StatusLabel(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestTaskLatestDuration(t *testing.T) {
	tests := []struct {
		name       string
		executions []models.Execution
		want       string
	}{
		{
			name:       "empty executions returns empty",
			executions: []models.Execution{},
			want:       "",
		},
		{
			name: "returns duration from latest execution with duration",
			executions: []models.Execution{
				{ID: "e1", DurationMs: 5000},
				{ID: "e2", DurationMs: 12000},
			},
			want: "12s",
		},
		{
			name: "skips executions without duration",
			executions: []models.Execution{
				{ID: "e1", DurationMs: 3000},
				{ID: "e2", DurationMs: 0},
			},
			want: "3s",
		},
		{
			name: "formats minutes correctly",
			executions: []models.Execution{
				{ID: "e1", DurationMs: 125000}, // 2m 5s
			},
			want: "2m 5s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TaskLatestDuration(tt.executions)
			if got != tt.want {
				t.Errorf("TaskLatestDuration() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTaskThreadStatusIndicator_Completed(t *testing.T) {
	task := &models.Task{
		ID:     "task1",
		Status: models.StatusCompleted,
	}
	executions := []models.Execution{
		{ID: "e1", Status: models.ExecCompleted, DurationMs: 5000},
	}

	var buf bytes.Buffer
	err := TaskThreadStatusIndicator(task, executions).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	body := buf.String()
	if body == "" {
		t.Fatal("expected non-empty output for completed task")
	}
	if !bytes.Contains(buf.Bytes(), []byte("Task completed")) {
		t.Error("expected 'Task completed' text in output")
	}
	if !bytes.Contains(buf.Bytes(), []byte("text-success")) {
		t.Error("expected success styling class")
	}
	if !bytes.Contains(buf.Bytes(), []byte("5s")) {
		t.Error("expected duration '5s' in output")
	}
}

func TestTaskThreadStatusIndicator_Failed(t *testing.T) {
	task := &models.Task{
		ID:     "task1",
		Status: models.StatusFailed,
	}
	executions := []models.Execution{
		{ID: "e1", Status: models.ExecFailed, DurationMs: 3000},
	}

	var buf bytes.Buffer
	err := TaskThreadStatusIndicator(task, executions).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	body := buf.String()
	if body == "" {
		t.Fatal("expected non-empty output for failed task")
	}
	if !bytes.Contains(buf.Bytes(), []byte("Task failed")) {
		t.Error("expected 'Task failed' text in output")
	}
	if !bytes.Contains(buf.Bytes(), []byte("text-error")) {
		t.Error("expected error styling class")
	}
}

func TestTaskThreadStatusIndicator_Running_NoIndicator(t *testing.T) {
	task := &models.Task{
		ID:     "task1",
		Status: models.StatusRunning,
	}
	executions := []models.Execution{
		{ID: "e1", Status: models.ExecRunning},
	}

	var buf bytes.Buffer
	err := TaskThreadStatusIndicator(task, executions).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	body := buf.String()
	if body != "" {
		t.Errorf("expected empty output for running task, got %q", body)
	}
}

func TestTaskThreadStatusIndicator_Pending_NoIndicator(t *testing.T) {
	task := &models.Task{
		ID:     "task1",
		Status: models.StatusPending,
	}
	executions := []models.Execution{}

	var buf bytes.Buffer
	err := TaskThreadStatusIndicator(task, executions).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	body := buf.String()
	if body != "" {
		t.Errorf("expected empty output for pending task, got %q", body)
	}
}
