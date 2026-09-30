package handler

import (
	"context"
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
	h, _, configs := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, configs)
	agent.Provider = models.ProviderOpenAI
	project := createProject(t, h, "Preparation failure")
	task := createTask(t, h, project.ID, "steering")
	exec := createExec(t, h, task.ID, agent.ID)
	checkpoint := []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[{"type":"message","role":"user","content":"original"}]}`}}
	require.NoError(t, h.execRepo.ReplaceReasoningReplay(ctx, exec.ID, "", checkpoint))
	_, err := h.execRepo.DB().ExecContext(ctx, `CREATE TRIGGER fail_replay_insert BEFORE INSERT ON execution_replay_messages BEGIN SELECT RAISE(FAIL, 'injected replay failure'); END`)
	require.NoError(t, err)
	params := streamingResponseParams{ExecID: exec.ID, Agent: *agent, Message: "previous steer", ChatHistory: []models.Execution{{ID: exec.ID + "-steering-context-1", ReplayMessages: checkpoint}}}
	batch := preparedSteeringBatch{inputs: []models.ThreadInput{{Content: "unsent steer"}}}
	_, err = h.prepareClaimedSteeringInputs(ctx, &params, "previous answer", batch, nil)
	require.ErrorContains(t, err, "injected replay failure")
	require.Equal(t, "previous steer", params.Message)
}

func TestSteeringPreparationFailureStopsHandler(t *testing.T) {
	h, _, configs := setupTestHandler(t)
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
		_, err := h.execRepo.DB().ExecContext(ctx, `CREATE TRIGGER fail_steering_prepare BEFORE UPDATE ON thread_inputs WHEN NEW.input_mode = 'steering' BEGIN SELECT RAISE(FAIL, 'injected preparation failure'); END`)
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
