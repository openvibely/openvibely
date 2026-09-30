package handler

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestSteeringReplayPreservesLateContinuation(t *testing.T) {
	h, _, configs := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, configs)
	project := createProject(t, h, "Late steering replay")
	task := createTask(t, h, project.ID, "steering")
	exec := createExec(t, h, task.ID, agent.ID)
	checkpoint := []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[{"type":"message","role":"user","content":"first steer"}]}`}}
	require.NoError(t, h.execRepo.ReplaceReasoningReplay(ctx, exec.ID, "", checkpoint))
	params := streamingResponseParams{
		ExecID: exec.ID, Message: "late steer", steeringOutputCursor: "first answer",
		ChatHistory: []models.Execution{{ID: exec.ID + "-steering-context-1", ReplayMessages: checkpoint}},
	}
	require.NoError(t, h.persistSteeringReplayHistory(ctx, params, "first answerlate answer"))
	saved, err := h.execRepo.ReplayMessagesByExecutionIDs(ctx, []string{exec.ID})
	require.NoError(t, err)
	require.Len(t, saved[exec.ID], 2)
	require.Equal(t, checkpoint[0], saved[exec.ID][0])
	require.Equal(t, "late steer", saved[exec.ID][1].UserContent)
	require.Equal(t, "late answer", saved[exec.ID][1].AssistantContent)

	// A second late continuation must retain the first one too.
	params.ChatHistory[0].ReplayMessages = saved[exec.ID]
	params.Message = "another steer"
	params.steeringOutputCursor = "first answerlate answer"
	require.NoError(t, h.persistSteeringReplayHistory(ctx, params, "first answerlate answeranother answer"))
	saved, err = h.execRepo.ReplayMessagesByExecutionIDs(ctx, []string{exec.ID})
	require.NoError(t, err)
	require.Len(t, saved[exec.ID], 3)
	require.Equal(t, "another steer", saved[exec.ID][2].UserContent)
	require.Equal(t, "another answer", saved[exec.ID][2].AssistantContent)
}
