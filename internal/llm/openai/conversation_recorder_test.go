package openai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestResponsesRecorderAfterSteeringSavesOnlyCurrentContext(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t).Stores
	task := &models.Task{ProjectID: "default", Title: "recorder", Category: models.CategoryChat, Status: models.StatusCompleted}
	require.NoError(t, stores.Tasks.Create(ctx, task))
	exec := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "inspect", Output: "ORIGINAL_TOOL_FACT"}
	require.NoError(t, stores.Executions.Create(ctx, exec))
	require.NoError(t, stores.Executions.AppendOutput(ctx, exec.ID, exec.Output))
	// Existing archives are ignored and removed on the next normal transcript write.
	require.NoError(t, stores.Executions.ReplaceReasoningReplay(ctx, exec.ID, "", []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[],"responses_compaction_sources":{"old":[]}}`}}))
	compacted := []any{map[string]any{"type": "compaction", "encrypted_content": "opaque"}}
	a := New(nil, stores.Executions, nil, nil)
	require.NoError(t, a.responsesHistoryRecorder(exec.ID)(ctx, compacted))
	require.NoError(t, stores.ThreadInputs.CommitLocalSteering(ctx, persistence.LocalSteeringCommit{ExecutionID: exec.ID, ResponsesHistory: compacted}))
	require.NoError(t, a.responsesHistoryRecorder(exec.ID)(ctx, compacted))
	rows, err := stores.Executions.ReplayMessagesByExecutionIDs(ctx, []string{exec.ID})
	require.NoError(t, err)
	require.Contains(t, rows[exec.ID][0].TranscriptJSON, "opaque")
	require.NotContains(t, rows[exec.ID][0].TranscriptJSON, "responses_compaction_sources")
	saved, err := stores.Executions.GetByID(ctx, exec.ID)
	require.NoError(t, err)
	require.Equal(t, "ORIGINAL_TOOL_FACT", saved.Output)
}

// Embed the interface so only the persistence boundary under test is replaced.
type recorderStore struct {
	persistence.ExecutionStore
	raw    string
	err    error
	calls  int
	ctxErr error
}

func (s *recorderStore) SaveConversationReplay(ctx context.Context, id string, history any, metadata models.ExecutionReplayMessage) (string, error) {
	raw, err := json.Marshal(history)
	if err != nil {
		return "", &persistence.ReplayEncodingError{Err: err}
	}
	encoded := string(raw)
	metadata.TranscriptJSON = encoded
	if err := s.ReplaceReasoningReplay(ctx, id, metadata.ReasoningContent, []models.ExecutionReplayMessage{metadata}); err != nil {
		return "", err
	}
	return encoded, nil
}

func (s *recorderStore) ReplaceReasoningReplay(ctx context.Context, id, reasoning string, messages []models.ExecutionReplayMessage) error {
	s.calls++
	s.ctxErr = ctx.Err()
	if s.err != nil {
		return s.err
	}
	s.raw = messages[0].TranscriptJSON
	return nil
}

func TestResponsesRecorderSerializationCompatibility(t *testing.T) {
	for _, items := range [][]any{
		nil, {},
		{map[string]any{"type": "message", "content": "<script>& café \u2028\u2029\n\""}},
		{json.RawMessage(` { "type": "function_call_output", "output": "<>&" } `)},
		{map[string]any{"type": "compaction", "encrypted_content": "opaque"}, map[string]any{"text": string([]byte{0xff})}},
	} {
		first, err := json.Marshal(map[string]any{"responses_input": items})
		require.NoError(t, err)
		legacy, err := json.Marshal(json.RawMessage(first))
		require.NoError(t, err)
		store := &recorderStore{}
		adapter := New(nil, store, nil, nil)
		recovered := ""
		ctx, cancel := context.WithCancel(llmcontracts.WithResponsesRecovery(context.Background(), func(raw string) {
			require.Equal(t, 1, store.calls, "recovery follows persistence")
			require.Equal(t, raw, store.raw)
			recovered = raw
		}))
		cancel() // Saving must still survive caller cancellation.
		require.NoError(t, adapter.responsesHistoryRecorder("exec")(ctx, items))
		require.NoError(t, store.ctxErr)
		require.Equal(t, string(legacy), store.raw)
		require.Equal(t, string(first), recovered)
	}
}

func TestResponsesRecorderFailureDoesNotAdvanceRecovery(t *testing.T) {
	failure := errors.New("storage failed")
	for _, tc := range []struct {
		name  string
		items []any
		err   error
		calls int
	}{
		{"encoding", []any{make(chan int)}, nil, 0},
		{"invalid raw JSON", []any{json.RawMessage(`{`)}, nil, 0},
		{"persistence", []any{"message"}, failure, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &recorderStore{err: tc.err}
			a := New(nil, store, nil, nil)
			ctx := llmcontracts.WithResponsesRecovery(context.Background(), func(string) { t.Error("recovery advanced after failure") })
			err := a.responsesHistoryRecorder("exec")(ctx, tc.items)
			require.Error(t, err)
			if tc.err != nil {
				require.ErrorIs(t, err, failure)
				require.Contains(t, err.Error(), "save OpenAI conversation")
			}
			require.Equal(t, tc.calls, store.calls)
		})
	}
	require.Nil(t, New(nil, nil, nil, nil).responsesHistoryRecorder("exec"))
	require.Nil(t, New(nil, &recorderStore{}, nil, nil).responsesHistoryRecorder(""))
}

func BenchmarkResponsesRecorderSerialization(b *testing.B) {
	items := []any{map[string]any{"type": "message", "content": strings.Repeat("history <>& text\n", 65536)}}
	for _, legacy := range []bool{true, false} {
		name := "serialize-once"
		if legacy {
			name = "legacy-double-marshal"
		}
		b.Run(name, func(b *testing.B) {
			store := &recorderStore{}
			a := New(nil, store, nil, nil)
			var recovery string
			ctx := llmcontracts.WithResponsesRecovery(context.Background(), func(raw string) { recovery = raw })
			record := a.responsesHistoryRecorder("exec")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if legacy {
					raw, err := json.Marshal(map[string]any{"responses_input": items})
					if err != nil {
						b.Fatal(err)
					}
					if err = a.saveConversation(ctx, "exec", json.RawMessage(raw)); err != nil {
						b.Fatal(err)
					}
					llmcontracts.RecordResponsesRecovery(ctx, string(raw))
				} else if err := record(ctx, items); err != nil {
					b.Fatal(err)
				}
			}
			if recovery != store.raw {
				b.Fatal("recovery differs from persisted transcript")
			}
		})
	}
}
