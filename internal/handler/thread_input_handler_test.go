package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

// threadInputRepo is wired in NewTestContext (derived from execRepo.DB()).

func TestCancelThreadInput_NonExistentID(t *testing.T) {
	tc := NewTestContext(t)
	// CancelPending on a missing ID returns ErrInputNotPending.
	// Handler should return 200 with the hidden-row fragment so HTMX removes
	// the stale composer row rather than leaving it stuck.
	rec := tc.HTTP().Post("/thread-inputs/nonexistent-input/cancel").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
	if got := rec.Header().Get(threadInputMutationStatusHeader); got != "not_pending" {
		t.Fatalf("cancel of non-existent row status header = %q, want not_pending", got)
	}
	if !strings.Contains(rec.Body.String(), `id="thread-input-nonexistent-input"`) {
		t.Error("cancel of non-existent row should return the hidden-row fragment for stale UI cleanup")
	}
}

func TestCancelThreadInput_SuccessReportsCancelled(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	p := tc.CreateProject().Build()
	task := tc.CreateTask(p.ID).Build()
	queued := &models.ThreadInput{
		Scope:       models.ThreadInputScopeTask,
		ProjectID:   p.ID,
		TaskID:      task.ID,
		InputMode:   models.ThreadInputModeQueued,
		InputStatus: models.ThreadInputPending,
		Content:     "cancel me",
	}
	if err := tc.handler.threadInputRepo.CreateQueued(ctx, queued); err != nil {
		t.Fatalf("create queued input: %v", err)
	}

	rec := tc.HTTP().Post("/thread-inputs/" + queued.ID + "/cancel").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
	if got := rec.Header().Get(threadInputMutationStatusHeader); got != "cancelled" {
		t.Fatalf("successful cancel status header = %q, want cancelled", got)
	}
	stored, err := tc.handler.threadInputRepo.GetByID(ctx, queued.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored == nil || stored.InputStatus != models.ThreadInputCancelled {
		t.Fatalf("stored input = %#v, want cancelled", stored)
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
	if err := tc.handler.threadInputRepo.MarkApplied(ctx, queued.ID, exec.ID, exec.ID); err != nil {
		t.Fatalf("mark applied: %v", err)
	}
	stored, err := tc.handler.threadInputRepo.GetByID(ctx, queued.ID)
	if err != nil {
		t.Fatalf("load applied input: %v", err)
	}
	if stored == nil || stored.InputStatus != models.ThreadInputApplied {
		t.Fatalf("stored input before cancellation = %#v, want applied", stored)
	}

	// Now cancel should return 200 with the hidden-row fragment (row already consumed).
	rec := tc.HTTP().Post("/thread-inputs/" + queued.ID + "/cancel").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
	if got := rec.Header().Get(threadInputMutationStatusHeader); got != "not_pending" {
		t.Fatalf("cancel of already-applied row status header = %q, want not_pending", got)
	}
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
	if got := rec.Header().Get(threadInputMutationStatusHeader); got != "not_pending" {
		t.Fatalf("cancel of prepared steering status header = %q, want not_pending", got)
	}
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

func TestEditThreadInput(t *testing.T) {
	for _, scope := range []models.ThreadInputScope{models.ThreadInputScopeTask, models.ThreadInputScopeChat} {
		t.Run(string(scope), func(t *testing.T) {
			tc := NewTestContext(t)
			ctx := context.Background()
			p := tc.CreateProject().Build()
			task := tc.CreateTask(p.ID).Build()
			input := &models.ThreadInput{Scope: scope, ProjectID: p.ID, TaskID: task.ID, Content: "original"}
			require.NoError(t, tc.handler.threadInputRepo.CreateQueued(ctx, input))
			endpoint := "/thread-inputs/" + input.ID + "/edit"
			rec := tc.HTTP().Post(endpoint).WithForm(url.Values{"content": {"  revised\nmessage  "}}).Execute()
			tc.Assert(rec).StatusCode(http.StatusOK)
			stored, err := tc.handler.threadInputRepo.GetByID(ctx, input.ID)
			require.NoError(t, err)
			require.Equal(t, "revised\nmessage", stored.Content)
			require.Equal(t, input.QueuePosition, stored.QueuePosition)
			require.Equal(t, models.ThreadInputPending, stored.InputStatus)
			require.Equal(t, input.AttachmentSessionID, stored.AttachmentSessionID)
			rec = tc.HTTP().Post(endpoint).WithForm(url.Values{"content": {" \n "}}).Execute()
			tc.Assert(rec).StatusCode(http.StatusBadRequest)
			_, err = tc.handler.threadInputRepo.CancelPending(ctx, input.ID)
			require.NoError(t, err)
			rec = tc.HTTP().Post(endpoint).WithForm(url.Values{"content": {"too late"}}).Execute()
			tc.Assert(rec).StatusCode(http.StatusConflict)
			stored, err = tc.handler.threadInputRepo.GetByID(ctx, input.ID)
			require.NoError(t, err)
			require.Equal(t, "revised\nmessage", stored.Content)
		})
	}
}

func TestEditThreadInputSteeringConsumptionGuard(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	p := tc.CreateProject().Build()
	task := tc.CreateTask(p.ID).Build()
	agent, err := tc.llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	require.NoError(t, tc.execRepo.Create(ctx, exec))
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: p.ID, TaskID: task.ID, Content: "original", RunExecutionID: exec.ID, TurnID: exec.ID, ExpectedTurnID: exec.ID}
	require.NoError(t, tc.handler.threadInputRepo.CreateSteeringForActiveExecution(ctx, input, exec.ID))
	endpoint := "/thread-inputs/" + input.ID + "/edit"
	holdEndpoint := "/thread-inputs/" + input.ID + "/edit-hold"
	tc.Assert(tc.HTTP().Post(holdEndpoint).Execute()).StatusCode(http.StatusOK)
	held, holdErr := tc.handler.threadInputRepo.PreparePendingSteering(ctx, exec.ID, exec.ID)
	require.NoError(t, holdErr)
	require.Empty(t, held)
	tc.Assert(tc.HTTP().Post(holdEndpoint).WithForm(url.Values{"hold": {"false"}}).Execute()).StatusCode(http.StatusOK)
	rec := tc.HTTP().Post(endpoint).WithForm(url.Values{"content": {"revised steering"}}).Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
	prepared, err := tc.handler.threadInputRepo.PreparePendingSteering(ctx, exec.ID, exec.ID)
	require.NoError(t, err)
	require.Len(t, prepared, 1)
	require.Equal(t, "revised steering", prepared[0].Content)
	rec = tc.HTTP().Post(endpoint).WithForm(url.Values{"content": {"too late"}}).Execute()
	tc.Assert(rec).StatusCode(http.StatusConflict)
	stored, err := tc.handler.threadInputRepo.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, "revised steering", stored.Content)
}

func TestEditingHoldsQueuedMessageAfterTurnFinishes(t *testing.T) {
	for _, scope := range []models.ThreadInputScope{models.ThreadInputScopeTask, models.ThreadInputScopeChat} {
		t.Run(string(scope), func(t *testing.T) {
			tc := NewTestContext(t)
			ctx := context.Background()
			p := tc.CreateProject().Build()
			task := tc.CreateTask(p.ID).Build()
			input := &models.ThreadInput{Scope: scope, ProjectID: p.ID, TaskID: task.ID, Content: "original"}
			require.NoError(t, tc.handler.threadInputRepo.CreateQueued(ctx, input))
			second := &models.ThreadInput{Scope: scope, ProjectID: p.ID, TaskID: task.ID, Content: "second"}
			require.NoError(t, tc.handler.threadInputRepo.CreateQueued(ctx, second))
			endpoint := "/thread-inputs/" + input.ID
			tc.Assert(tc.HTTP().Post(endpoint + "/edit-hold").Execute()).StatusCode(http.StatusOK)
			tc.handler.startNextQueuedTurnAfter(ctx, streamingResponseParams{ProjectID: p.ID, TaskID: task.ID, IsTaskFollowup: scope == models.ThreadInputScopeTask}, "")
			stored, err := tc.handler.threadInputRepo.GetByID(ctx, input.ID)
			require.NoError(t, err)
			require.Equal(t, models.ThreadInputPending, stored.InputStatus)
			var next *models.ThreadInput
			if scope == models.ThreadInputScopeTask {
				next, err = tc.handler.threadInputRepo.FindOldestQueuedForTask(ctx, task.ID)
			} else {
				next, err = tc.handler.threadInputRepo.FindOldestQueuedForChat(ctx, p.ID)
			}
			require.NoError(t, err)
			require.Nil(t, next, "held head must also prevent later messages overtaking it")
			// A consumer with a stale reference must be rejected atomically as well.
			require.ErrorIs(t, tc.handler.threadInputRepo.MarkApplied(ctx, input.ID, "", ""), repository.ErrInputNotPending)
			tc.Assert(tc.HTTP().Post(endpoint + "/edit").WithForm(url.Values{"content": {"edited"}}).Execute()).StatusCode(http.StatusOK)
			if scope == models.ThreadInputScopeTask {
				next, err = tc.handler.threadInputRepo.FindOldestQueuedForTask(ctx, task.ID)
			} else {
				next, err = tc.handler.threadInputRepo.FindOldestQueuedForChat(ctx, p.ID)
			}
			require.NoError(t, err)
			require.NotNil(t, next)
			require.Equal(t, input.ID, next.ID)
			require.Equal(t, "edited", next.Content)
		})
	}
}

func TestFinishingQueuedEditResumesAfterCompletedTurn(t *testing.T) {
	for _, save := range []bool{false, true} {
		t.Run(fmt.Sprint("save=", save), func(t *testing.T) {
			tc := NewTestContext(t)
			ctx := context.Background()
			p := tc.CreateProject().Build()
			task := tc.CreateTask(p.ID).Build()
			agent, err := tc.llmConfigRepo.GetDefault(ctx)
			require.NoError(t, err)
			input := &models.ThreadInput{Scope: models.ThreadInputScopeChat, ProjectID: p.ID, TaskID: task.ID, AgentConfigID: agent.ID, Content: "original"}
			require.NoError(t, tc.handler.threadInputRepo.CreateQueued(ctx, input))
			endpoint := "/thread-inputs/" + input.ID
			tc.Assert(tc.HTTP().Post(endpoint + "/edit-hold").Execute()).StatusCode(http.StatusOK)
			want := "original"
			if save {
				want = "revised"
				tc.Assert(tc.HTTP().Post(endpoint + "/edit").WithForm(url.Values{"content": {want}, "resume": {"true"}}).Execute()).StatusCode(http.StatusOK)
			} else {
				tc.Assert(tc.HTTP().Post(endpoint + "/edit-hold").WithForm(url.Values{"hold": {"false"}}).Execute()).StatusCode(http.StatusOK)
			}
			require.Eventually(t, func() bool {
				stored, err := tc.handler.threadInputRepo.GetByID(ctx, input.ID)
				if err != nil || stored.InputStatus != models.ThreadInputApplied {
					return false
				}
				execution, err := tc.execRepo.GetByID(ctx, stored.RunExecutionID)
				return err == nil && execution != nil && execution.PromptSent == want && execution.Status != models.ExecRunning
			}, 5*time.Second, 10*time.Millisecond)
		})
	}
}

func TestExpiredEditorResumesQueuedMessage(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	p := tc.CreateProject().Build()
	task := tc.CreateTask(p.ID).Build()
	agent, err := tc.llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	input := &models.ThreadInput{Scope: models.ThreadInputScopeChat, ProjectID: p.ID, TaskID: task.ID, AgentConfigID: agent.ID, Content: "last saved text"}
	require.NoError(t, tc.handler.threadInputRepo.CreateQueued(ctx, input))
	endpoint := "/thread-inputs/" + input.ID + "/edit-hold"
	tc.Assert(tc.HTTP().Post(endpoint).Execute()).StatusCode(http.StatusOK)
	tc.Assert(tc.HTTP().Post(endpoint).WithForm(url.Values{"renew": {"true"}}).Execute()).StatusCode(http.StatusNoContent)
	tc.handler.RecoverExpiredInputEdits(ctx)
	next, err := tc.handler.threadInputRepo.FindOldestQueuedForChat(ctx, p.ID)
	require.NoError(t, err)
	require.Nil(t, next)
	// Simulate a crashed browser without waiting for wall-clock expiry.
	_, err = tc.execRepo.DB().ExecContext(ctx, `UPDATE thread_inputs SET edit_hold_until = datetime('now', '-1 second') WHERE id = ?`, input.ID)
	require.NoError(t, err)
	tc.Assert(tc.HTTP().Post(endpoint).WithForm(url.Values{"renew": {"true"}}).Execute()).StatusCode(http.StatusConflict)
	tc.handler.RecoverExpiredInputEdits(ctx)
	require.Eventually(t, func() bool {
		stored, err := tc.handler.threadInputRepo.GetByID(ctx, input.ID)
		if err != nil || stored.InputStatus != models.ThreadInputApplied {
			return false
		}
		execution, err := tc.execRepo.GetByID(ctx, stored.RunExecutionID)
		return err == nil && execution != nil && execution.PromptSent == "last saved text" && execution.Status != models.ExecRunning
	}, 5*time.Second, 10*time.Millisecond)
}

func TestEditHoldOwnerRoutes(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().Build()
	input := &models.ThreadInput{Scope: models.ThreadInputScopeChat, ProjectID: project.ID, Content: "original"}
	require.NoError(t, tc.handler.threadInputRepo.CreateQueued(ctx, input))
	base := "/thread-inputs/" + input.ID
	tc.Assert(tc.HTTP().Post(base + "/edit-hold").WithForm(url.Values{"edit_token": {"owner"}}).Execute()).StatusCode(http.StatusOK)
	for _, values := range []url.Values{{"edit_token": {"other"}}, {"edit_token": {"other"}, "renew": {"true"}}, {"edit_token": {"other"}, "hold": {"false"}}} {
		tc.Assert(tc.HTTP().Post(base + "/edit-hold").WithForm(values).Execute()).StatusCode(http.StatusConflict)
	}
	tc.Assert(tc.HTTP().Post(base + "/edit").WithForm(url.Values{"edit_token": {"other"}, "content": {"wrong"}}).Execute()).StatusCode(http.StatusConflict)
	tc.Assert(tc.HTTP().Post(base + "/edit").WithForm(url.Values{"edit_token": {"owner"}, "content": {"saved"}}).Execute()).StatusCode(http.StatusOK)
}
