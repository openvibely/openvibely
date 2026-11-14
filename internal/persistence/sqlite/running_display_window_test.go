package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func runningWindowFixture(n int) string {
	var s strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&s, "[Using tool: shell | echo %d]\n[Tool shell done]\ncafé %d\n[/Tool]\n", i, i)
	}
	return s.String()
}

func TestRunningDisplayWindowsRoundTrip(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	ctx := t.Context()
	source := "[Using tool: read_file | pending]\n" + runningWindowFixture(5000)
	require.NoError(t, repo.AppendOutput(ctx, owner.ExecutionID, source))
	initial, err := repo.GetDisplayExecutionTail(ctx, owner.ExecutionID)
	require.NoError(t, err)
	require.NotNil(t, initial.DisplayTail)
	tail := *initial.DisplayTail
	require.Equal(t, int64(len(source)), tail.EndOffset)
	require.Less(t, len(tail.Text), 10000)
	require.Contains(t, tail.Context, "pending")
	require.Contains(t, tail.Text, "echo 4999")
	require.NotContains(t, tail.Text, "echo 0]")
	// Replay remains absolute: the omitted prefix must not be replayed to the view.
	delta := "[Tool read_file done]\nlate result\n[/Tool]\n"
	require.NoError(t, repo.AppendOutput(ctx, owner.ExecutionID, delta))
	replay, err := repo.ReadOutputAfter(ctx, owner.ExecutionID, int(tail.EndOffset))
	require.NoError(t, err)
	require.Equal(t, delta, replay.Output)
	joined := tail.Text + delta
	context := tail.Context
	for tail.Before != 0 {
		tail, err = repo.ReadProjectDisplayTailRevision(ctx, "default", owner.ExecutionID, tail.Before, 1024*1024, tail.Revision)
		require.NoError(t, err)
		joined = tail.Text + strings.TrimPrefix(joined, context)
		context = tail.Context
	}
	require.Equal(t, source+delta, joined)
	// Completion leaves the live cursor usable when the public bytes are unchanged.
	require.NoError(t, repo.Complete(ctx, owner.ExecutionID, models.ExecCompleted, source+delta, "", 0, 0))
	_, err = repo.ReadProjectDisplayTailRevision(ctx, "default", owner.ExecutionID, initial.DisplayTail.Before, 1024*1024, initial.DisplayTail.Revision)
	require.NoError(t, err)
	_, err = repo.ReadProjectDisplayTailRevision(ctx, "other", owner.ExecutionID, initial.DisplayTail.Before, 1024, initial.DisplayTail.Revision)
	require.Error(t, err)
}

func TestRunningDisplayWindowRewriteAndOpaqueBlocks(t *testing.T) {
	source := runningWindowFixture(200)
	tail, err := runningDisplayWindow(source, 0, 2048, "")
	require.NoError(t, err)
	require.LessOrEqual(t, len(tail.Text)-len(tail.Context), 2048)
	_, err = runningDisplayWindow("X"+source[1:], tail.Before, 2048, tail.Revision)
	require.ErrorIs(t, err, sql.ErrNoRows)
	for _, block := range []string{
		"[Thinking]\n" + strings.Repeat("reason ", 1000),
		"[Using tool: shell | large]\n[Tool shell done]\n" + strings.Repeat("result ", 1000),
		"```text\n" + strings.Repeat("code ", 1000),
		"`unfinished example\n[Using tool: shell | example]\n" + source,
	} {
		t.Run(fmt.Sprint(len(block)), func(t *testing.T) {
			tail, err := runningDisplayWindow(source+block, 0, 1024, "")
			require.NoError(t, err)
			require.True(t, strings.HasSuffix(tail.Text, block), "an oversized indivisible block must remain intact")
		})
	}
}

func TestRunningWindowMarkdownAndAliases(t *testing.T) {
	source := "Intro with `closed code`.\n```go\nfmt.Println(1)\n```\n[Using tool: Read | old]\n[Tool read_file done]\nold result\n[/Tool]\n" + runningWindowFixture(5000)
	tail, err := runningDisplayWindow(source, 0, 1024*1024, "")
	require.NoError(t, err)
	require.Less(t, len(tail.Text), 10000, "closed Markdown must not force full loading")
	require.NotContains(t, tail.Context, "old", "aliases must not leave a finished call pending")
	for _, prefix := range []string{"`open\n", "[ref]: https://example.com\n", "[Using tool: shell | legacy]\n[Tool shell done: old]\n"} {
		tail, err = runningDisplayWindow(prefix+runningWindowFixture(200), 0, 1024, "")
		require.NoError(t, err)
		require.Equal(t, prefix+runningWindowFixture(200), tail.Text, "preserve ambiguous or global Markdown/legacy context")
	}
}

func TestRunningWindowContextToolOffsets(t *testing.T) {
	prefix := "Prose café 🌍\n"
	header := "  [Using tool: read_file | pending]\n"
	source := prefix + header + runningWindowFixture(200)
	tail, err := runningDisplayWindow(source, 0, 1024, "")
	require.NoError(t, err)
	require.Equal(t, header, tail.Context)
	require.Equal(t, map[int]int64{2: int64(len(prefix) + 2)}, tail.ContextToolOffsets)
	for tail.Before != 0 {
		tail, err = runningDisplayWindow(source, tail.Before, 1024, tail.Revision)
		require.NoError(t, err)
	}
	require.Empty(t, tail.ContextToolOffsets)
	require.Equal(t, source[:len(tail.Text)], tail.Text)
}
