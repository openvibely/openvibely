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
	"github.com/openvibely/openvibely/internal/transcript"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
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
	require.NoError(t, a.responsesHistoryChangesRecorder(exec.ID)(ctx, compacted, 0))
	require.NoError(t, stores.ThreadInputs.CommitLocalSteering(ctx, persistence.LocalSteeringCommit{ExecutionID: exec.ID, ResponsesHistory: compacted}))
	require.NoError(t, a.responsesHistoryChangesRecorder(exec.ID)(ctx, compacted, 0))
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
	raw      string
	err      error
	calls    int
	ctxErr   error
	heads    map[string]models.ConversationChange
	sequence int64
}

func (s *recorderStore) saveLegacySnapshot(ctx context.Context, id string, history any, metadata models.ExecutionReplayMessage) (string, error) {
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
		ctx, cancel := context.WithCancel(llmcontracts.WithResponsesRecovery(context.Background(), func(items []any) {
			rawBytes, _ := json.Marshal(map[string]any{"responses_input": items})
			raw := string(rawBytes)
			require.Equal(t, 1, store.calls, "recovery follows persistence")
			require.Equal(t, raw, store.raw)
			recovered = raw
		}))
		cancel() // Saving must still survive caller cancellation.
		require.NoError(t, adapter.responsesHistoryChangesRecorder("exec")(ctx, items, 0))
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
			ctx := llmcontracts.WithResponsesRecovery(context.Background(), func([]any) { t.Error("recovery advanced after failure") })
			err := a.responsesHistoryChangesRecorder("exec")(ctx, tc.items, 0)
			require.Error(t, err)
			if tc.err != nil {
				require.ErrorIs(t, err, failure)
				require.Contains(t, err.Error(), "save OpenAI conversation")
			}
			require.Equal(t, tc.calls, store.calls)
		})
	}
	require.Nil(t, New(nil, nil, nil, nil).responsesHistoryChangesRecorder("exec"))
	require.Nil(t, New(nil, &recorderStore{}, nil, nil).responsesHistoryChangesRecorder(""))
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
			ctx := llmcontracts.WithResponsesRecovery(context.Background(), func(items []any) {
				rawBytes, _ := json.Marshal(map[string]any{"responses_input": items})
				raw := string(rawBytes)
				recovery = raw
			})
			record := a.responsesHistoryChangesRecorder("exec")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if legacy {
					raw, err := json.Marshal(map[string]any{"responses_input": items})
					if err != nil {
						b.Fatal(err)
					}
					if _, err = store.saveLegacySnapshot(ctx, "exec", json.RawMessage(raw), models.ExecutionReplayMessage{}); err != nil {
						b.Fatal(err)
					}
					llmcontracts.RecordResponsesRecovery(ctx, items)
				} else if err := record(ctx, items, 0); err != nil {
					b.Fatal(err)
				}
			}
			if recovery != store.raw {
				b.Fatal("recovery differs from persisted transcript")
			}
		})
	}
}

func (s *recorderStore) ReadConversationHeads(context.Context, models.ConversationOwner, models.ConversationLane, int64) ([]models.ConversationChange, error) {
	var result []models.ConversationChange
	for _, c := range s.heads {
		result = append(result, c)
	}
	return result, nil
}
func (s *recorderStore) ConversationRevision(_ context.Context, _ models.ConversationOwner, _ models.ConversationLane, id string, _ int64) (int64, error) {
	return s.heads[id].Sequence, nil
}
func (s *recorderStore) AppendConversationChanges(ctx context.Context, _ models.ConversationOwner, changes []models.ConversationChange) ([]models.ConversationChange, error) {
	s.calls++
	s.ctxErr = ctx.Err()
	if s.err != nil {
		return nil, s.err
	}
	if s.heads == nil {
		s.heads = make(map[string]models.ConversationChange)
	}
	for i := range changes {
		s.sequence++
		changes[i].Sequence = s.sequence
		s.heads[changes[i].ItemID] = changes[i]
	}
	var records []models.ConversationChange
	for _, c := range s.heads {
		records = append(records, c)
	}
	messages, err := transcript.RestoreReplayRecords(records)
	if err != nil {
		return nil, err
	}
	if len(messages) > 0 {
		s.raw = messages[0].TranscriptJSON
	}
	return changes, nil
}

func TestResponsesExchangeRecorderDeltaRecoveryAndRewrite(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t).Stores
	task := &models.Task{ProjectID: "default", Title: "delta", Category: models.CategoryCompleted, Status: models.StatusCompleted}
	require.NoError(t, stores.Tasks.Create(ctx, task))
	old := &models.Execution{TaskID: task.ID, Status: models.ExecCompleted}
	require.NoError(t, stores.Executions.Create(ctx, old))
	current := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	require.NoError(t, stores.Executions.Create(ctx, current))
	prefix := []any{map[string]any{"type": "message", "role": "user", "content": "old large image"}}
	adapter := New(nil, stores.Executions, nil, nil)
	require.NoError(t, adapter.responsesHistoryChangesRecorder(old.ID)(ctx, prefix, 0))
	reader := stores.Executions.(interface {
		ReplayRowsByExecutionIDs(context.Context, []string) (map[string][]models.ExecutionReplayMessage, error)
	})
	loaded, err := reader.ReplayRowsByExecutionIDs(ctx, []string{old.ID})
	require.NoError(t, err)
	old.ReplayMessages = loaded[old.ID]
	var recovery []any
	ctx = llmcontracts.WithResponsesRecovery(ctx, func(items []any) { recovery = items })
	begin, save := adapter.responsesExchangeRecorder(current.ID, []models.Execution{*old})
	durable, err := begin(ctx, prefix)
	require.NoError(t, err)
	require.True(t, durable)
	delta := []any{map[string]any{"type": "message", "role": "user", "content": "new request"}}
	require.NoError(t, save(ctx, openaiclient.ResponsesReplayUpdate{Items: delta}))
	heads, err := stores.Executions.ReadConversationHeads(ctx, models.ConversationOwner{ExecutionID: current.ID}, "", 0)
	require.NoError(t, err)
	raw, err := transcript.RestoreReplayRecords(heads)
	require.NoError(t, err)
	require.Contains(t, raw[0].TranscriptJSON, "responses_delta")
	require.NotContains(t, raw[0].TranscriptJSON, "old large image")
	require.Equal(t, append(append([]any(nil), prefix...), delta...), recovery)
	// Atomic steering writes full state externally; the recorder must reload its
	// guard and restore delta storage without losing accepted input.
	all := append(append([]any(nil), prefix...), delta...)
	require.NoError(t, stores.ThreadInputs.CommitLocalSteering(ctx, persistence.LocalSteeringCommit{ExecutionID: current.ID, ResponsesHistory: all}))
	delta = append(delta, map[string]any{"type": "function_call_output", "call_id": "done", "output": "tool finished"})
	require.NoError(t, save(ctx, openaiclient.ResponsesReplayUpdate{Items: delta, FirstChanged: 1}))
	loaded, err = reader.ReplayRowsByExecutionIDs(ctx, []string{current.ID})
	require.NoError(t, err)
	require.Len(t, loaded[current.ID][0].Rows.Items, 2)
	require.NotContains(t, loaded[current.ID][0].FullTranscriptJSON(), "responses_base_execution_ids")
	replacement := []any{map[string]any{"type": "compaction", "encrypted_content": "replacement"}}
	require.NoError(t, save(ctx, openaiclient.ResponsesReplayUpdate{Items: replacement, ReplacesHistory: true}))
	require.Equal(t, replacement, recovery)
	loaded, err = reader.ReplayRowsByExecutionIDs(ctx, []string{current.ID})
	require.NoError(t, err)
	require.Len(t, loaded[current.ID][0].Rows.Items, 1)
}

func TestResponsesExchangeRecorderStartsIndependentBase(t *testing.T) {
	for _, reason := range []string{"cross-task", "failed"} {
		t.Run(reason, func(t *testing.T) {
			ctx := context.Background()
			stores := teststores.New(t).Stores
			task := &models.Task{ProjectID: "default", Title: "base", Category: models.CategoryCompleted, Status: models.StatusCompleted}
			require.NoError(t, stores.Tasks.Create(ctx, task))
			source := &models.Execution{TaskID: task.ID, Status: models.ExecCompleted}
			require.NoError(t, stores.Executions.Create(ctx, source))
			a := New(nil, stores.Executions, nil, nil)
			prefix := []any{map[string]any{"type": "message", "role": "user", "content": "base"}}
			require.NoError(t, a.responsesHistoryChangesRecorder(source.ID)(ctx, prefix, 0))
			reader := stores.Executions.(interface {
				ReplayRowsByExecutionIDs(context.Context, []string) (map[string][]models.ExecutionReplayMessage, error)
			})
			rows, err := reader.ReplayRowsByExecutionIDs(ctx, []string{source.ID})
			require.NoError(t, err)
			source.ReplayMessages = rows[source.ID]
			targetTask := task.ID
			switch reason {
			case "cross-task":
				other := &models.Task{ProjectID: "default", Title: "other", Category: models.CategoryCompleted, Status: models.StatusCompleted}
				require.NoError(t, stores.Tasks.Create(ctx, other))
				targetTask = other.ID
			case "failed":
				source.Status = models.ExecFailed
			}
			e := &models.Execution{TaskID: targetTask, Status: models.ExecRunning}
			require.NoError(t, stores.Executions.Create(ctx, e))
			begin, save := a.responsesExchangeRecorder(e.ID, []models.Execution{*source})
			durable, err := begin(ctx, prefix)
			require.NoError(t, err)
			require.False(t, durable)
			require.NoError(t, save(ctx, openaiclient.ResponsesReplayUpdate{Items: append(append([]any(nil), prefix...), map[string]any{"role": "user", "content": "next"}), ReplacesHistory: true}))
			heads, err := stores.Executions.ReadConversationHeads(ctx, models.ConversationOwner{ExecutionID: e.ID}, "", 0)
			require.NoError(t, err)
			raw, err := transcript.RestoreReplayRecords(heads)
			require.NoError(t, err)
			require.Contains(t, raw[0].TranscriptJSON, "responses_input")
			require.NotContains(t, raw[0].TranscriptJSON, "responses_base_execution_ids")
		})
	}
}
