package sqlite

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/transcript"
	"github.com/stretchr/testify/require"
)

func TestCheckpointReferencesSurviveSourceChangesAndDeletion(t *testing.T) {
	db, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	recorder := transcript.NewRecorder(repo, owner)
	header := transcript.ReplayHeader{Format: "array"}
	require.NoError(t, recorder.Save(ctx, header, []any{"before", "kept", "tail"}))
	checkpoint := models.ChatCompactionCheckpoint{ScopeType: "task", ScopeID: "reference-test", SourceExecutionID: owner.ExecutionID, Summary: "summary", History: []models.Execution{{ID: owner.ExecutionID, ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `[ "kept", "tail" ]`}}}}}
	require.NoError(t, repo.UpsertChatCompactionCheckpoint(ctx, checkpoint))
	countRefs := func(want int) {
		t.Helper()
		var count int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM conversation_checkpoint_references`).Scan(&count))
		require.Equal(t, want, count)
	}
	restore := func() {
		t.Helper()
		got, err := repo.GetChatCompactionCheckpoint(ctx, checkpoint.ScopeType, checkpoint.ScopeID)
		require.NoError(t, err)
		require.Len(t, got.History, 1)
		require.JSONEq(t, `["kept","tail"]`, got.History[0].ReplayMessages[0].TranscriptJSON)
	}
	countRefs(2)
	restore()
	var bytes int
	require.NoError(t, db.QueryRow(`SELECT sum(length(payload)) FROM conversation_items WHERE sequence IN (SELECT checkpoint_sequence FROM conversation_checkpoint_references)`).Scan(&bytes))
	require.Equal(t, 4, bytes) // Two empty placeholders, not duplicate provider text.
	var before, after int64
	require.NoError(t, db.QueryRow(`SELECT max(sequence) FROM conversation_items`).Scan(&before))
	require.NoError(t, repo.UpsertChatCompactionCheckpoint(ctx, checkpoint))
	require.NoError(t, db.QueryRow(`SELECT max(sequence) FROM conversation_items`).Scan(&after))
	require.Equal(t, before, after)
	// Internal paged readers must also resolve references, not return placeholders.
	page, err := repo.ReadConversationPage(ctx, models.ConversationOwner{ScopeType: checkpoint.ScopeType, ScopeID: checkpoint.ScopeID}, models.ConversationProvider, 0, -1, "", 100)
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	require.Equal(t, json.RawMessage(`"kept"`), page.Items[0].Payload)
	checkpointOwner := models.ConversationOwner{ScopeType: checkpoint.ScopeType, ScopeID: checkpoint.ScopeID}
	heads, err := repo.ReadConversationHeads(ctx, checkpointOwner, models.ConversationProvider, 0)
	require.NoError(t, err)
	retried, err := repo.AppendConversationChanges(ctx, checkpointOwner, heads)
	require.NoError(t, err)
	require.Equal(t, heads, retried)
	countRefs(2)
	require.NoError(t, recorder.Save(ctx, header, []any{"before", "changed", "tail"}))
	countRefs(1)
	restore()
	_, err = db.Exec(`DELETE FROM executions WHERE id=?`, owner.ExecutionID)
	require.NoError(t, err)
	countRefs(0)
	restore()
}

func TestCheckpointReferencesRetirementIsAtomic(t *testing.T) {
	db, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	r := transcript.NewRecorder(repo, owner)
	require.NoError(t, r.Save(ctx, transcript.ReplayHeader{Format: "array"}, []any{"retained"}))
	checkpoint := models.ChatCompactionCheckpoint{ScopeType: "task", ScopeID: "atomic-references", SourceExecutionID: owner.ExecutionID, Strategy: "local_summary_inflight", History: []models.Execution{{ID: owner.ExecutionID, ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `["retained"]`}}}}}
	// Force failure after references have been written and the source retired.
	_, err := db.Exec(`CREATE TRIGGER fail_empty_replay BEFORE INSERT ON conversation_items WHEN NEW.execution_id IS NOT NULL AND NEW.item_id='replay-guard' BEGIN SELECT RAISE(ABORT,'retirement failed'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, repo.SaveInTurnCompactionCheckpoint(ctx, checkpoint), "retirement failed")
	got, err := repo.GetChatCompactionCheckpoint(ctx, checkpoint.ScopeType, checkpoint.ScopeID)
	require.NoError(t, err)
	require.Nil(t, got)
	replay, err := repo.ReplayMessagesByExecutionIDs(ctx, []string{owner.ExecutionID})
	require.NoError(t, err)
	require.Equal(t, `["retained"]`, replay[owner.ExecutionID][0].TranscriptJSON)
	_, err = db.Exec(`DROP TRIGGER fail_empty_replay`)
	require.NoError(t, err)
	require.NoError(t, repo.SaveInTurnCompactionCheckpoint(ctx, checkpoint))
	got, err = repo.GetChatCompactionCheckpoint(ctx, checkpoint.ScopeType, checkpoint.ScopeID)
	require.NoError(t, err)
	require.Equal(t, `["retained"]`, got.History[0].ReplayMessages[0].TranscriptJSON)
	replay, err = repo.ReplayMessagesByExecutionIDs(ctx, []string{owner.ExecutionID})
	require.NoError(t, err)
	require.Equal(t, `[]`, replay[owner.ExecutionID][0].TranscriptJSON)
}

func TestCheckpointReferenceReplacementReleasesBorrowers(t *testing.T) {
	db, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	require.NoError(t, transcript.NewRecorder(repo, owner).Save(ctx, transcript.ReplayHeader{Format: "array"}, []any{"native"}))
	checkpoint := models.ChatCompactionCheckpoint{ScopeType: "task", ScopeID: "native-ref", SourceExecutionID: owner.ExecutionID, CompatibilityKey: "a", ProviderStateJSON: `["native"]`}
	require.NoError(t, repo.UpsertChatCompactionCheckpoint(ctx, checkpoint))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM conversation_checkpoint_references`).Scan(&count))
	require.Equal(t, 1, count)
	checkpoint.CompatibilityKey = "b"
	require.NoError(t, repo.UpsertChatProviderSessionState(ctx, checkpoint))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM conversation_checkpoint_references`).Scan(&count))
	require.Zero(t, count)
	got, err := repo.GetChatCompactionCheckpoint(ctx, checkpoint.ScopeType, checkpoint.ScopeID)
	require.NoError(t, err)
	require.Empty(t, got.ProviderStateJSON)
}

func TestStreamingFlushRollbackAndByteCursor(t *testing.T) {
	db, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.AppendOutput(ctx, owner.ExecutionID, "hello "))
	_, err := db.Exec(`CREATE TRIGGER fail_stream_append BEFORE INSERT ON execution_stream_text BEGIN SELECT RAISE(ABORT,'flush failed'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, repo.AppendOutput(ctx, owner.ExecutionID, "lost"), "flush failed")
	got, err := repo.ReadOutputAfter(ctx, owner.ExecutionID, 0)
	require.NoError(t, err)
	require.Equal(t, "hello ", got.Output)
	_, err = db.Exec(`DROP TRIGGER fail_stream_append`)
	require.NoError(t, err)
	// The fast path must measure JSON's replacement characters, not invalid bytes.
	require.NoError(t, repo.AppendOutput(ctx, owner.ExecutionID, "世\xff\xfe"))
	got, err = repo.ReadOutputAfter(ctx, owner.ExecutionID, len("hello "))
	require.NoError(t, err)
	require.Equal(t, "世��", got.Output)
	require.EqualValues(t, len("hello 世��"), got.Total)
	require.NoError(t, repo.FinalizeOutput(ctx, owner.ExecutionID, "replaced"))
	require.NoError(t, repo.AppendOutput(ctx, owner.ExecutionID, " tail"))
	got, err = repo.ReadOutputAfter(ctx, owner.ExecutionID, 0)
	require.NoError(t, err)
	require.Equal(t, "replaced tail", got.Output)
	require.NoError(t, repo.Complete(ctx, owner.ExecutionID, models.ExecCompleted, got.Output, "", 0, 0))
	require.NoError(t, repo.AppendOutput(ctx, owner.ExecutionID, "late"))
	got, err = repo.ReadOutputAfter(ctx, owner.ExecutionID, 0)
	require.NoError(t, err)
	require.Equal(t, "replaced tail", got.Output)
}

func TestCheckpointDigestSkipsUnchangedHistoryAndInvalidatesOnMutation(t *testing.T) {
	db, repo, _ := conversationTestRepo(t)
	ctx := t.Context()
	checkpoint := models.ChatCompactionCheckpoint{ScopeType: "task", ScopeID: "digest", Summary: "first", History: []models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `["original"]`}}, ReasoningContent: "reason", RetainedUserMessage: json.RawMessage(`{"role":"user","content":"retained"}`)}}}
	require.NoError(t, repo.UpsertChatCompactionCheckpoint(ctx, checkpoint))
	_, err := db.Exec(`CREATE TRIGGER fail_checkpoint_change BEFORE INSERT ON conversation_items WHEN NEW.checkpoint_scope_id='digest' BEGIN SELECT RAISE(ABORT,'checkpoint write failed'); END`)
	require.NoError(t, err)
	checkpoint.Summary = "summary only"
	require.NoError(t, repo.UpsertChatCompactionCheckpoint(ctx, checkpoint))
	checkpoint.History[0].ReasoningContent = "changed reason"
	require.ErrorContains(t, repo.UpsertChatCompactionCheckpoint(ctx, checkpoint), "checkpoint write failed")
	got, err := repo.GetChatCompactionCheckpoint(ctx, "task", "digest")
	require.NoError(t, err)
	require.Equal(t, "summary only", got.Summary)
	require.Equal(t, "reason", got.History[0].ReasoningContent)
	_, err = db.Exec(`DROP TRIGGER fail_checkpoint_change`)
	require.NoError(t, err)
	// An independent transactional writer must invalidate the unchanged-history shortcut.
	checkpoint.History[0].ReasoningContent = "reason"
	owner := models.ConversationOwner{ScopeType: "task", ScopeID: "digest"}
	heads, err := repo.ReadConversationHeads(ctx, owner, models.ConversationProvider, 0)
	require.NoError(t, err)
	require.Len(t, heads, 1)
	change := heads[0]
	change.PreviousRevision = change.Sequence
	change.Sequence = 0
	change.MutationID = "independent-checkpoint-write"
	change.Payload = json.RawMessage(`"changed by another writer"`)
	_, err = repo.AppendConversationChanges(ctx, owner, []models.ConversationChange{change})
	require.NoError(t, err)
	require.NoError(t, repo.UpsertChatCompactionCheckpoint(ctx, checkpoint))
	got, err = repo.GetChatCompactionCheckpoint(ctx, "task", "digest")
	require.NoError(t, err)
	require.JSONEq(t, `["original"]`, got.History[0].ReplayMessages[0].TranscriptJSON)
	require.JSONEq(t, string(checkpoint.History[0].RetainedUserMessage), string(got.History[0].RetainedUserMessage))
}

func TestCheckpointRangeSurvivesMiddleEditDeletionAndRollback(t *testing.T) {
	for _, inTurn := range []bool{false, true} {
		t.Run(map[bool]string{false: "source-edit", true: "in-turn-rollback"}[inTurn], func(t *testing.T) {
			db, repo, owner := conversationTestRepo(t)
			ctx := t.Context()
			recorder := transcript.NewRecorder(repo, owner)
			items := []any{"zero", "one", "two", "three", "four", "five", "six", "seven"}
			require.NoError(t, recorder.Save(ctx, transcript.ReplayHeader{Format: "array"}, items))
			raw, err := json.Marshal(items[1:7])
			require.NoError(t, err)
			checkpoint := models.ChatCompactionCheckpoint{ScopeType: "task", ScopeID: "range", SourceExecutionID: owner.ExecutionID, Strategy: "local_summary_inflight", History: []models.Execution{{ID: owner.ExecutionID, ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}}}}}
			restore := func() {
				t.Helper()
				got, err := repo.GetChatCompactionCheckpoint(ctx, "task", "range")
				require.NoError(t, err)
				require.NotNil(t, got)
				require.JSONEq(t, string(raw), got.History[0].ReplayMessages[0].TranscriptJSON)
			}
			if inTurn {
				_, err := db.Exec(`CREATE TRIGGER fail_range_guard BEFORE INSERT ON conversation_items WHEN NEW.item_id='replay-guard' BEGIN SELECT RAISE(ABORT,'range rollback'); END`)
				require.NoError(t, err)
				require.ErrorContains(t, repo.SaveInTurnCompactionCheckpoint(ctx, checkpoint), "range rollback")
				got, err := repo.GetChatCompactionCheckpoint(ctx, "task", "range")
				require.NoError(t, err)
				require.Nil(t, got)
				_, err = db.Exec(`DROP TRIGGER fail_range_guard`)
				require.NoError(t, err)
				require.NoError(t, repo.SaveInTurnCompactionCheckpoint(ctx, checkpoint))
				restore()
			} else {
				require.NoError(t, repo.UpsertChatCompactionCheckpoint(ctx, checkpoint))
				var ranges int
				require.NoError(t, db.QueryRow(`SELECT count(*) FROM conversation_checkpoint_ranges`).Scan(&ranges))
				require.Equal(t, 1, ranges)
				restore()
				items[3] = "changed"
				require.NoError(t, recorder.SaveChanged(ctx, transcript.ReplayHeader{Format: "array"}, items, 3))
				restore()
				require.NoError(t, db.QueryRow(`SELECT count(*) FROM conversation_checkpoint_ranges`).Scan(&ranges))
				require.Zero(t, ranges)
			}
			_, err = db.Exec(`DELETE FROM executions WHERE id=?`, owner.ExecutionID)
			require.NoError(t, err)
			restore()
		})
	}
}
