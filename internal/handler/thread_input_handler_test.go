package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

// threadInputRepo is wired in NewTestContext (derived from execRepo.DB()).

func TestCancelThreadInput_NonExistentID(t *testing.T) {
	tc := NewTestContext(t)
	// CancelPending on a missing ID returns ErrInputNotPending.
	// Handler should return 200 with the hidden-row fragment so HTMX removes
	// the stale composer row rather than leaving it stuck.
	rec := tc.HTTP().Post("/thread-inputs/nonexistent-input/cancel").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
	if !strings.Contains(rec.Body.String(), `id="thread-input-nonexistent-input"`) {
		t.Error("cancel of non-existent row should return the hidden-row fragment for stale UI cleanup")
	}
}

func TestCancelThreadInput_AlreadyAppliedReturnsRemovedRow(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	p := tc.CreateProject().Build()
	task := tc.CreateTask(p.ID).Build()

	agent, _ := tc.llmConfigRepo.GetDefault(ctx)
	if agent == nil {
		t.Skip("no default agent configured")
	}

	// Create an active execution so we can create a queued input.
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "active run",
	}
	if err := tc.execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}

	queued := &models.ThreadInput{
		Scope:     models.ThreadInputScopeTask,
		ProjectID: p.ID,
		TaskID:    task.ID,
		InputMode: models.ThreadInputModeQueued,
		Content:   "pending follow-up",
	}
	if err := tc.handler.threadInputRepo.CreateQueued(ctx, queued); err != nil {
		t.Fatalf("create queued input: %v", err)
	}

	// Mark it applied (simulating the promotion path).
	if err := tc.handler.threadInputRepo.MarkApplied(ctx, queued.ID, exec.ID, exec.ID); err != nil {
		t.Fatalf("mark applied: %v", err)
	}

	// Now cancel should return 200 with the hidden-row fragment (row already consumed).
	rec := tc.HTTP().Post("/thread-inputs/" + queued.ID + "/cancel").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
	body := rec.Body.String()
	if !strings.Contains(body, `id="thread-input-`+queued.ID) {
		t.Errorf("cancel of already-applied row should return the hidden-row fragment for UI cleanup, body=%q", body)
	}
}

func TestCancelThreadInput_PreparedSteeringReturnsRemovedRow(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	p := tc.CreateProject().Build()
	task := tc.CreateTask(p.ID).Build()

	agent, _ := tc.llmConfigRepo.GetDefault(ctx)
	if agent == nil {
		t.Skip("no default agent configured")
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "active run",
	}
	if err := tc.execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}

	steering := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      p.ID,
		TaskID:         task.ID,
		InputMode:      models.ThreadInputModeSteering,
		InputStatus:    models.ThreadInputPending,
		RunExecutionID: exec.ID,
		TurnID:         exec.ID,
		ExpectedTurnID: exec.ID,
		Content:        "rebase against main",
	}
	if err := tc.handler.threadInputRepo.CreateSteeringForActiveExecution(ctx, steering, exec.ID); err != nil {
		t.Fatalf("create steering: %v", err)
	}

	// Prepare the steering row (simulates what PreparePendingTextSteering does: clears expected_turn_id).
	prepared, err := tc.handler.threadInputRepo.PreparePendingSteering(ctx, exec.ID, exec.ID)
	if err != nil || len(prepared) == 0 {
		t.Fatalf("prepare steering: err=%v rows=%d", err, len(prepared))
	}

	// The prepared/in-flight row is protected from cancellation in the DB.
	// The handler should still return 200 with the hidden-row fragment so
	// any stale UI entry is removed from the composer.
	rec := tc.HTTP().Post("/thread-inputs/" + steering.ID + "/cancel").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
	body := rec.Body.String()
	if !strings.Contains(body, `id="thread-input-`+steering.ID) {
		t.Errorf("cancel of in-flight prepared steering should return hidden-row fragment for UI cleanup, body=%q", body)
	}

	// Verify the DB row is still pending (DB protection is intact).
	stored, err := tc.handler.threadInputRepo.GetByID(ctx, steering.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored == nil || stored.InputStatus != models.ThreadInputPending {
		t.Errorf("DB row should remain pending (in-flight protection intact), got %#v", stored)
	}
}

func TestChatQueuedInputSteer_NonExistentID(t *testing.T) {
	tc := NewTestContext(t)
	// GetByID returns (nil, nil) for missing ID → 409 "queued input is no longer pending"
	rec := tc.HTTP().Post("/chat/queued/nonexistent-input/steer").Execute()
	tc.Assert(rec).StatusCode(http.StatusConflict)
}

func TestChatQueuedInputSteer_WithAttachmentsShowsSteeringAttachmentIndicator(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	p := tc.CreateProject().Build()
	task := tc.CreateTask(p.ID).WithCategory(models.CategoryChat).Build()
	agent, _ := tc.llmConfigRepo.GetDefault(ctx)
	if agent == nil {
		t.Skip("no default agent configured")
	}
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	if err := tc.execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}
	queued := &models.ThreadInput{
		Scope:               models.ThreadInputScopeChat,
		ProjectID:           p.ID,
		RunExecutionID:      exec.ID,
		InputMode:           models.ThreadInputModeQueued,
		InputStatus:         models.ThreadInputPending,
		Content:             "convert attached queued chat",
		AttachmentSessionID: "chat-convert-session",
	}
	if err := tc.handler.threadInputRepo.CreateQueued(ctx, queued); err != nil {
		t.Fatalf("create queued input: %v", err)
	}

	rec := tc.HTMX().Post("/chat/queued/" + queued.ID + "/steer").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
	body := rec.Body.String()
	if !strings.Contains(body, "Steering pending") || !strings.Contains(body, "Attachments included") || !strings.Contains(body, `aria-label="Attachments included with this steering instruction"`) {
		t.Fatalf("converted chat queued row should show steering attachment indicator, got: %q", body)
	}
	stored, err := tc.handler.threadInputRepo.GetByID(ctx, queued.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored == nil || stored.InputMode != models.ThreadInputModeSteering || stored.AttachmentSessionID != "chat-convert-session" {
		t.Fatalf("converted row = %#v, want steering with attachment session", stored)
	}
}

func TestTaskThreadQueuedInputSteer_NonExistentInput(t *testing.T) {
	tc := NewTestContext(t)
	p := tc.CreateProject().Build()
	task := tc.CreateTask(p.ID).Build()
	// Input doesn't exist → GetByID returns nil → 409
	rec := tc.HTTP().Post("/tasks/" + task.ID + "/thread/queued/nonexistent-input/steer").Execute()
	tc.Assert(rec).StatusCode(http.StatusConflict)
}

func TestPublishThreadInputCancelledEvent_NilInput(t *testing.T) {
	tc := NewTestContext(t)
	// publishThreadInputCancelledEvent with nil input is a no-op; call it to exercise the nil guard.
	tc.handler.publishThreadInputCancelledEvent(nil)
}
