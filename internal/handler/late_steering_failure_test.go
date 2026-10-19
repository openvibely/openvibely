package handler

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestLateSteeringCommitSurvivesModelFailureCleanup(t *testing.T) {
	h, _, configs := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, configs)
	project := createProject(t, h, "Late steering")
	task := createTask(t, h, project.ID, "steering", func(task *models.Task) {
		task.Category, task.Status, task.AgentID = models.CategoryActive, models.StatusRunning, &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(exec *models.Execution) { exec.Status = models.ExecRunning })
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, ExpectedTurnID: exec.ID, Content: "late steer"}
	require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, input, exec.ID))
	params := streamingResponseParams{ExecID: exec.ID}
	batch, err := h.claimPendingSteeringInputs(ctx, &params)
	require.NoError(t, err)
	require.Equal(t, 1, batch.count())
	commit := h.preparedSteeringCommit(params, batch)
	history := []any{map[string]any{"type": "message", "role": "user", "content": "late steer"}}
	require.NoError(t, commit(ctx, history))
	h.requeuePendingSteeringForExecution(ctx, exec.ID)
	stored, err := h.threadInputRepo.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputApplied, stored.InputStatus)
	require.Equal(t, models.ThreadInputModeSteering, stored.InputMode)
	replay, err := h.execRepo.ReplayMessagesByExecutionIDs(ctx, []string{exec.ID})
	require.NoError(t, err)
	require.Contains(t, replay[exec.ID][0].TranscriptJSON, "late steer")
	require.NoError(t, commit(ctx, append(history, map[string]any{"type": "message", "role": "assistant", "content": "answer"})))
}

func TestLateSteeringPreparationPropagatesCheckpointFailure(t *testing.T) {
	h, _, configs, env := setupTestHandlerWithStores(t)
	ctx := context.Background()
	agent := createAgent(t, configs)
	agent.Provider = models.ProviderOpenAI
	project := createProject(t, h, "Preparation failure")
	task := createTask(t, h, project.ID, "steering")
	exec := createExec(t, h, task.ID, agent.ID)
	checkpoint := []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[{"type":"message","role":"user","content":"original"}]}`}}
	require.NoError(t, h.execRepo.ReplaceReasoningReplay(ctx, exec.ID, "", checkpoint))
	err := env.Fixtures.FailExecutionReplayWrites(ctx)
	require.NoError(t, err)
	params := streamingResponseParams{ExecID: exec.ID, Agent: *agent, Message: "previous steer", ChatHistory: []models.Execution{{ID: exec.ID + "-steering-context-1", ReplayMessages: checkpoint}}}
	batch := preparedSteeringBatch{inputs: []models.ThreadInput{{Content: "unsent steer"}}}
	_, err = h.prepareClaimedSteeringInputs(ctx, &params, "previous answer", batch, nil)
	require.ErrorContains(t, err, "injected replay failure")
	require.Equal(t, "previous steer", params.Message)
}

func TestSteeringPreparationFailureStopsHandler(t *testing.T) {
	h, _, configs, env := setupTestHandlerWithStores(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, configs)
	project := createProject(t, h, "Stop on preparation failure")
	task := createTask(t, h, project.ID, "steering", func(task *models.Task) {
		task.Category, task.Status, task.AgentID = models.CategoryActive, models.StatusRunning, &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(exec *models.Execution) { exec.Status = models.ExecRunning })
	mock := testutil.NewMockLLMCaller()
	mock.Response = "answer"
	h.llmSvc.SetLLMCaller(mock)
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, ExpectedTurnID: exec.ID, Content: "unsent steer"}
	mock.OnCall = func(context.Context, testutil.MockLLMCall) {
		require.Equal(t, 1, mock.CallCount())
		require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, input, exec.ID))
		err := env.Fixtures.FailSteeringPreparation(ctx)
		require.NoError(t, err)
	}
	h.processStreamingResponse(streamingResponseParams{ExecID: exec.ID, TaskID: task.ID, ProjectID: project.ID, Agent: *agent, Message: "original", IsTaskFollowup: true, suppressQueuedTurnPromotion: true})
	require.Equal(t, 1, mock.CallCount())
	stored, err := h.execRepo.GetByID(ctx, exec.ID)
	require.NoError(t, err)
	require.Equal(t, models.ExecFailed, stored.Status)
	require.Contains(t, stored.ErrorMessage, "injected preparation failure")
	queued, err := h.threadInputRepo.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputPending, queued.InputStatus)
	require.Equal(t, models.ThreadInputModeQueued, queued.InputMode)
}

func TestSteeringAttachmentCommitRetriesAfterFailure(t *testing.T) {
	for _, failure := range []string{"delivery", "second-upload"} {
		t.Run(failure, func(t *testing.T) {
			h, _, configs := setupTestHandler(t)
			useTempUploadsDir(t)
			ctx := context.Background()
			agent := createAgent(t, configs)
			project := createProject(t, h, "Attachment retry")
			task := createTask(t, h, project.ID, "steering", func(task *models.Task) {
				task.Category, task.Status, task.AgentID = models.CategoryActive, models.StatusRunning, &agent.ID
			})
			exec := createExec(t, h, task.ID, agent.ID, func(exec *models.Execution) { exec.Status = models.ExecRunning })
			writeUpload := func(session string) {
				dir := filepath.Join(uploadsDir, "chat", "pending", session)
				require.NoError(t, os.MkdirAll(dir, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "doc.pdf"), []byte(session), 0o644))
			}
			for _, session := range []string{"first", "second"} {
				input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, ExpectedTurnID: exec.ID, Content: session, AttachmentSessionID: session}
				require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, input, exec.ID))
			}
			writeUpload("first")
			if failure == "delivery" {
				writeUpload("second")
			}
			params := streamingResponseParams{ExecID: exec.ID}
			batch, err := h.claimPendingSteeringInputs(ctx, &params)
			require.NoError(t, err)
			require.Equal(t, 2, batch.count())
			commit := h.preparedSteeringCommit(params, batch)
			// An unserializable checkpoint fails delivery after attachments are staged.
			require.Error(t, commit(ctx, []any{make(chan int)}))
			require.FileExists(t, filepath.Join(uploadsDir, "chat", "pending", "first", "doc.pdf"))
			rows, err := h.attachmentRepo.ListByTask(ctx, task.ID)
			require.NoError(t, err)
			require.Empty(t, rows)
			files, err := os.ReadDir(filepath.Join(uploadsDir, "chat", exec.ID))
			require.NoError(t, err)
			require.Empty(t, files)
			writeUpload("second")
			require.NoError(t, commit(ctx, nil))
			require.NoError(t, commit(ctx, nil), "successful commits may be called again")
			rows, err = h.attachmentRepo.ListByTask(ctx, task.ID)
			require.NoError(t, err)
			require.Len(t, rows, 2)
			require.NotEqual(t, rows[0].FilePath, rows[1].FilePath)
			for _, session := range []string{"first", "second"} {
				require.NoDirExists(t, filepath.Join(uploadsDir, "chat", "pending", session))
			}
		})
	}
}
