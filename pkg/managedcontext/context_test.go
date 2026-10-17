package managedcontext

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type message struct {
	role, text string
	toolResult bool
}

func TestRefreshRestoreAndDeduplicateWithoutLosingTools(t *testing.T) {
	read := func(m message) (string, string) {
		if m.toolResult {
			return "", ""
		}
		return m.role, m.text
	}
	makeMessage := func(s string) message { return message{role: "user", text: s} }
	ctx := With(context.Background(), "documents: edit documents\npreference.md: concise")
	items := []message{{role: "user", text: "summarize report.docx"}, {role: "assistant", text: "skill_view(documents)"}, {role: "user", text: "loaded skill body", toolResult: true}, {role: "user", text: "edit that file"}}
	items = Ensure(ctx, items, read, makeMessage)
	require.Len(t, items, 5)
	require.Equal(t, "edit that file", items[4].text)
	require.Equal(t, items, Ensure(ctx, items, read, makeMessage))
	changed := With(ctx, "documents: updated description")
	items = Ensure(changed, append(items[:4], append([]message{{role: "user", text: Text(ctx)}}, items[4:]...)...), read, makeMessage)
	require.Len(t, items, 5)
	require.True(t, items[2].toolResult)
	require.Contains(t, items[3].text, "updated description")
	// Compaction removed both index and loaded instructions: restore only the index.
	compacted := []message{{role: "user", text: "summary: report.docx was summarized"}, {role: "user", text: "edit that file"}}
	restored := Ensure(changed, compacted, read, makeMessage)
	require.Len(t, restored, 3)
	require.Equal(t, Text(changed), restored[1].text)
	require.NotContains(t, restored[1].text, "loaded skill body")
	require.Len(t, Ensure(With(ctx, ""), items, read, makeMessage), 4)
}

func TestIndexStaysAtOriginalPositionAcrossFollowupsAndToolCalls(t *testing.T) {
	read := func(m message) (string, string) {
		if m.toolResult {
			return "", ""
		}
		return m.role, m.text
	}
	makeMessage := func(s string) message { return message{role: "user", text: s} }
	ctx := With(context.Background(), "documents: edit Word documents\npreferences.md: preferences")
	original := []message{
		{role: "user", text: "Summarize this document"},
		{role: "user", text: Text(ctx)},
		{role: "assistant", text: "skill_view(document_word)"},
		{role: "user", text: "Word instructions", toolResult: true},
		{role: "assistant", text: "Here's the summary"},
	}
	items := append([]message(nil), original...)
	for _, next := range []message{
		{role: "user", text: "Change the title"},
		{role: "assistant", text: "edit_file(report.docx)"},
		{role: "user", text: "Title updated", toolResult: true},
	} {
		items = append(items, next)
		require.Equal(t, items, Ensure(ctx, items, read, makeMessage), "unchanged indexes must not move across model calls")
	}
	require.Equal(t, original, items[:len(original)])

	changed := With(ctx, "documents: updated description\npreferences.md: preferences")
	updated := Ensure(changed, items, read, makeMessage)
	want := append([]message(nil), items...)
	want[1] = makeMessage(Text(changed))
	require.Equal(t, want, updated, "replace only the changed index in place")

	duplicated := append(append([]message(nil), items...), makeMessage(Text(ctx)))
	require.Equal(t, items, Ensure(ctx, duplicated, read, makeMessage))
	// Keep the matching message even if a stale duplicate precedes it.
	duplicated = append([]message{makeMessage(Text(changed))}, items...)
	require.Equal(t, items, Ensure(ctx, duplicated, read, makeMessage))

	without := append(append([]message(nil), items[:1]...), items[2:]...)
	require.Equal(t, without, Ensure(With(ctx, ""), items, read, makeMessage))
	require.False(t, Contains(without, read))
	require.True(t, Contains(items, read))
	require.Equal(t, items, Ensure(context.Background(), items, read, makeMessage), "unmanaged calls do not rewrite history")
}

func TestSeedReplayMatchesFirstInsertionWithoutPinningAttachments(t *testing.T) {
	read := func(m message) (string, string) { return m.role, m.text }
	makeMessage := func(s string) message { return message{role: "user", text: s} }
	ctx := With(context.Background(), "document_word: Word documents")
	for _, multimodal := range []bool{false, true} {
		visible := []message{{role: "assistant", text: "earlier"}, {role: "user", text: "Summarize this document"}}
		if multimodal {
			// Structured attachment input is deliberately not exposed as plain text.
			visible[1].text = ""
		}
		replay := []message{{role: "user", text: "Summarize this document"}}
		seeded := SeedReplay(ctx, visible, replay, read, makeMessage)
		onWire := Ensure(ctx, visible, read, makeMessage)
		require.Len(t, seeded, 2)
		for i, m := range onWire[1:] {
			if m.text == Text(ctx) {
				require.Equal(t, m, seeded[i], "replay must retain the wire position relative to multimodal input")
			}
		}
		require.Equal(t, replay, SeedReplay(ctx, onWire, replay, read, makeMessage))
		require.Equal(t, replay, SeedReplay(With(ctx, ""), visible, replay, read, makeMessage))
	}
}
