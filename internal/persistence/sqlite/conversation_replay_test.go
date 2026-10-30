package sqlite

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/transcript"
	"github.com/stretchr/testify/require"
)

func TestRecorderPreservesMetadataAndDurableRecoveryOnFailure(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	recorder := transcript.NewRecorder(repo, owner)
	header := transcript.ReplayHeader{Format: "array", UserContent: "user", AssistantContent: "answer", ReasoningContent: "reason", ReasoningAuthoritative: true}
	require.NoError(t, recorder.Save(ctx, header, []any{"<>& café\n"}))
	rows, err := repo.ReplayMessagesByExecutionIDs(ctx, []string{owner.ExecutionID})
	require.NoError(t, err)
	require.Len(t, rows[owner.ExecutionID], 1)
	saved := rows[owner.ExecutionID][0]
	require.Equal(t, "user", saved.UserContent)
	require.Equal(t, "answer", saved.AssistantContent)
	require.Equal(t, "reason", saved.ReasoningContent)
	execution, err := repo.GetByID(ctx, owner.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, "reason", execution.ReasoningContent)
	recovery := recorder.Recovery()
	err = recorder.Save(ctx, header, []any{make(chan int)})
	var encoding *persistence.ReplayEncodingError
	require.ErrorAs(t, err, &encoding)
	require.Equal(t, recovery, recorder.Recovery())
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	err = recorder.Save(canceled, header, []any{"changed"})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, recovery, recorder.Recovery())
	rows, err = repo.ReplayMessagesByExecutionIDs(ctx, []string{owner.ExecutionID})
	require.NoError(t, err)
	require.Equal(t, saved, rows[owner.ExecutionID][0])
	header.AssistantContent = "final answer"
	require.NoError(t, recorder.Save(context.WithoutCancel(canceled), header, []any{"changed"}))
	rows, err = repo.ReplayMessagesByExecutionIDs(ctx, []string{owner.ExecutionID})
	require.NoError(t, err)
	require.Equal(t, models.ExecutionReplayMessage{UserContent: "user", AssistantContent: "final answer", ReasoningContent: "reason", TranscriptJSON: `["changed"]`}, rows[owner.ExecutionID][0])
}

func TestRowReplayMatchesLegacyAndCheckpoint(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	recorder := transcript.NewRecorder(repo, owner)
	header := transcript.ReplayHeader{Format: "envelope", ItemsKey: "responses_input", UserContent: "user"}
	require.NoError(t, recorder.Save(t.Context(), header, []any{map[string]any{"role": "user", "content": "hello"}, map[string]any{"type": "reasoning", "encrypted_content": "private"}}))
	rows, err := repo.ReplayRowsByExecutionIDs(t.Context(), []string{owner.ExecutionID})
	require.NoError(t, err)
	legacy, err := repo.ReplayMessagesByExecutionIDs(t.Context(), []string{owner.ExecutionID})
	require.NoError(t, err)
	got := rows[owner.ExecutionID][0]
	require.Empty(t, got.TranscriptJSON)
	require.JSONEq(t, legacy[owner.ExecutionID][0].TranscriptJSON, got.FullTranscriptJSON())
	h, items, err := transcript.SplitReplay(got)
	require.NoError(t, err)
	joined, err := transcript.JoinReplay(h, items)
	require.NoError(t, err)
	require.JSONEq(t, legacy[owner.ExecutionID][0].TranscriptJSON, joined.TranscriptJSON)
}
