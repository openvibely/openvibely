package openai

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestResponsesCompactionArchiveSurvivesRecorderRestart(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t).Stores
	task := &models.Task{ProjectID: "default", Title: "archive", Category: models.CategoryChat, Status: models.StatusCompleted}
	require.NoError(t, stores.Tasks.Create(ctx, task))
	exec := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "inspect"}
	require.NoError(t, stores.Executions.Create(ctx, exec))
	a := New(nil, stores.Executions, nil, nil)
	recorder := a.newResponsesRecorder(exec.ID)
	before := []any{
		map[string]any{"type": "message", "role": "user", "content": "inspect"},
		map[string]any{"type": "function_call", "call_id": "a", "name": "read_file", "arguments": "{}"},
		map[string]any{"type": "function_call_output", "call_id": "a", "output": "UNIQUE_TOOL_FACT"},
	}
	first := []any{map[string]any{"type": "compaction", "encrypted_content": "first"}}
	require.NoError(t, recorder.archive(ctx, before, first))
	require.NoError(t, recorder.history(ctx, first))
	// A new recorder must retain the first source while recording a second compaction.
	recorder = a.newResponsesRecorder(exec.ID)
	middle := append(first, map[string]any{"type": "message", "role": "assistant", "content": "SECOND_FACT"})
	second := []any{map[string]any{"type": "compaction", "encrypted_content": "second"}}
	require.NoError(t, recorder.archive(ctx, middle, second))
	require.NoError(t, recorder.history(ctx, append(second, map[string]any{"type": "message", "role": "user", "content": "continue"})))
	rows, err := stores.Executions.ReplayMessagesByExecutionIDs(ctx, []string{exec.ID})
	require.NoError(t, err)
	history := []models.Execution{{ID: exec.ID, ReplayMessages: rows[exec.ID]}}
	// Native OpenAI requests retain the opaque state and never send the archive.
	native, err := json.Marshal(HistoryInputItems(history))
	require.NoError(t, err)
	require.Contains(t, string(native), "second")
	require.NotContains(t, string(native), "UNIQUE_TOOL_FACT")
	expanded, err := ExpandCompactedHistory(history, CompactionSources(history))
	require.NoError(t, err)
	raw, err := json.Marshal(HistoryInputItems(expanded))
	require.NoError(t, err)
	require.Contains(t, string(raw), "UNIQUE_TOOL_FACT")
	require.Contains(t, string(raw), "SECOND_FACT")
	require.Contains(t, string(raw), "continue")
	require.NotContains(t, string(raw), "encrypted_content")
	require.Contains(t, history[0].ReplayMessages[0].TranscriptJSON, "encrypted_content")
}

func TestExpandCompactedHistoryRejectsMissingAndCyclicSources(t *testing.T) {
	raw := json.RawMessage(`{"type":"compaction","encrypted_content":"missing"}`)
	history := []models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[` + string(raw) + `]}`}}}}
	_, err := ExpandCompactedHistory(history, nil)
	require.ErrorContains(t, err, "readable history")
	_, err = ExpandCompactedHistory(history, map[string][]json.RawMessage{"missing": {raw}})
	require.ErrorContains(t, err, "readable history")
}
