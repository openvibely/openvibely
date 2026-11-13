package sqlite

import (
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestDisplayStreamWindowsRoundTripAndResume(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	ctx := t.Context()
	// A structured prefix followed by raw live chunks exercises both cursor kinds.
	prefix := "stored prefix\n"
	_, err := repo.AppendConversationChanges(ctx, owner, []models.ConversationChange{{ItemID: "prefix", Lane: models.ConversationDisplay, Operation: models.ConversationPut, Payload: []byte(`{"type":"text","text":"stored prefix\n"}`), MutationID: "prefix"}})
	require.NoError(t, err)
	var output strings.Builder
	output.WriteString(prefix)
	for i := 0; i < 80; i++ {
		chunk := fmt.Sprintf("text %d café\n[Using tool: shell | echo]\n[Tool shell done]\n%s\n[/Tool]\n", i, strings.Repeat("result\n", 1200))
		require.NoError(t, repo.AppendOutput(ctx, owner.ExecutionID, chunk))
		output.WriteString(chunk)
	}
	var joined string
	var cursor int64
	var revision string
	pages := 0
	for {
		page, err := repo.ReadProjectDisplayTail(ctx, "default", owner.ExecutionID, cursor, 65536)
		require.NoError(t, err)
		require.Equal(t, int64(output.Len()), page.EndOffset)
		if pages == 0 {
			revision = page.Revision
			require.NotZero(t, page.Before)
		} else {
			require.Equal(t, revision, page.Revision)
		}
		require.Equal(t, strings.Count(page.Text, "[Using tool:"), strings.Count(page.Text, "[Tool shell done]"))
		joined = page.Text + joined
		pages++
		if page.Before == 0 {
			break
		}
		require.NotEqual(t, cursor, page.Before)
		cursor = page.Before
		require.Less(t, pages, 100)
	}
	require.True(t, joined == output.String(), "pages must reconstruct the exact UTF-8 bytes")
	// Saved raw page cursors remain usable when more live bytes arrive.
	latest, err := repo.ReadProjectDisplayTail(ctx, "default", owner.ExecutionID, 0, 65536)
	require.NoError(t, err)
	require.NoError(t, repo.AppendOutput(ctx, owner.ExecutionID, "new live tail"))
	older, err := repo.ReadProjectDisplayTail(ctx, "default", owner.ExecutionID, latest.Before, 65536)
	require.NoError(t, err)
	require.Equal(t, latest.Revision, older.Revision)
	require.Equal(t, int64(output.Len()+len("new live tail")), older.EndOffset)
	require.NoError(t, repo.Complete(ctx, owner.ExecutionID, models.ExecCompleted, "", "", 0, 0))
	finished, err := repo.ReadProjectDisplayTail(ctx, "default", owner.ExecutionID, 0, 65536)
	require.NoError(t, err)
	require.NotEqual(t, revision, finished.Revision)
}

func TestDisplayPlanProjectionAcrossAppendsAndBlocks(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	ctx := t.Context()
	c := conversationChange("create-plan", "plan", `{"type":"text","text":"intro"}`)
	changes, err := repo.AppendConversationChanges(ctx, owner, []models.ConversationChange{c})
	require.NoError(t, err)
	c.Operation = models.ConversationAppend
	c.PreviousRevision = changes[0].Sequence
	c.MutationID = "start-plan"
	c.Payload = []byte(`{"text":"before <proposed_"}`)
	_, err = repo.AppendConversationChanges(ctx, owner, []models.ConversationChange{c})
	require.NoError(t, err)
	c = conversationChange("finish-plan", "next", `{"type":"text","text":"plan>complete approval</proposed_plan>after"}`)
	c.Position = 1
	_, err = repo.AppendConversationChanges(ctx, owner, []models.ConversationChange{c})
	require.NoError(t, err)
	page, err := repo.ReadProjectDisplayTail(ctx, "default", owner.ExecutionID, 0, 1)
	require.NoError(t, err)
	require.Equal(t, "<proposed_plan>complete approval</proposed_plan>\n", page.Plan)
}

func TestDisplayIndexFollowsEditsAndRemoval(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	ctx := t.Context()
	var records []models.ConversationChange
	for i := 0; i < 5; i++ {
		c := conversationChange(fmt.Sprint(i), fmt.Sprint(i), `{"type":"text","text":"original"}`)
		c.Position = int64(i)
		records = append(records, c)
	}
	written, err := repo.AppendConversationChanges(ctx, owner, records)
	require.NoError(t, err)
	check := func() {
		t.Helper()
		full, err := repo.GetDisplayExecution(ctx, owner.ExecutionID)
		require.NoError(t, err)
		var joined string
		var cursor int64
		for n := 0; ; n++ {
			require.Less(t, n, 10)
			page, err := repo.ReadProjectDisplayTail(ctx, "default", owner.ExecutionID, cursor, 1)
			require.NoError(t, err)
			require.Equal(t, int64(len(full.Output)), page.EndOffset)
			joined = page.Text + joined
			if page.Before == 0 {
				break
			}
			cursor = page.Before
		}
		require.Equal(t, full.Output, joined)
	}
	check()
	edit := records[3]
	edit.MutationID = "moved"
	edit.PreviousRevision = written[3].Sequence
	edit.Position = records[3].Position
	edit.Payload = []byte(`{"type":"text","text":"changed longer"}`)
	_, err = repo.AppendConversationChanges(ctx, owner, []models.ConversationChange{edit})
	require.NoError(t, err)
	check()
	remove := records[0]
	remove.MutationID = "removed"
	remove.PreviousRevision = written[0].Sequence
	remove.Operation = models.ConversationRemove
	remove.Payload = []byte(`{}`)
	_, err = repo.AppendConversationChanges(ctx, owner, []models.ConversationChange{remove})
	require.NoError(t, err)
	check()
}

func TestDisplayLivePlanRemainsOutsideWindow(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	ctx := t.Context()
	var full strings.Builder
	appendText := func(text string) {
		t.Helper()
		require.NoError(t, repo.AppendOutput(ctx, owner.ExecutionID, text))
		full.WriteString(text)
	}
	// Both opening and closing markers arrive one byte at a time.
	plan := "<proposed_plan>keep this approval</proposed_plan>"
	for _, r := range plan {
		appendText(string(r))
	}
	for i := 0; i < 40; i++ {
		appendText("text\n[Using tool: shell | echo]\n[Tool shell done]\n" + strings.Repeat("result\n", 1200) + "[/Tool]\n")
	}
	page, err := repo.ReadProjectDisplayTail(ctx, "default", owner.ExecutionID, 0, 65536)
	require.NoError(t, err)
	require.NotZero(t, page.Before)
	require.NotContains(t, page.Text, "keep this approval")
	require.Equal(t, plan+"\n", page.Plan)
	// Finalization replaces stream storage; it must not duplicate the plan.
	require.NoError(t, repo.Complete(ctx, owner.ExecutionID, models.ExecCompleted, full.String(), "", 0, 0))
	page, err = repo.ReadProjectDisplayTail(ctx, "default", owner.ExecutionID, 0, 65536)
	require.NoError(t, err)
	require.Equal(t, plan+"\n", page.Plan)
}

func TestDisplayPlanSpansStructuredAndLiveOutput(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	c := conversationChange("prefix", "prefix", `{"type":"text","text":"<proposed_"}`)
	_, err := repo.AppendConversationChanges(t.Context(), owner, []models.ConversationChange{c})
	require.NoError(t, err)
	require.NoError(t, repo.AppendOutput(t.Context(), owner.ExecutionID, "plan>approval</proposed_plan>"))
	page, err := repo.ReadProjectDisplayTail(t.Context(), "default", owner.ExecutionID, 0, 1)
	require.NoError(t, err)
	require.Equal(t, "<proposed_plan>approval</proposed_plan>\n", page.Plan)
}
