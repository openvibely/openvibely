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

func TestUpdatesPreserveHistoryAndRestoreAfterCompaction(t *testing.T) {
	read := func(m message) (string, string) {
		if m.toolResult {
			return "", ""
		}
		return m.role, m.text
	}
	makeMessage := func(s string) message { return message{role: "user", text: s} }
	ctx := With(context.Background(), "skills and memory v1")
	old := []message{makeMessage(Text(ctx)), {role: "assistant", text: "call"}, {role: "user", text: "result", toolResult: true}, makeMessage("followup")}
	require.Equal(t, old, Ensure(ctx, old, read, makeMessage))
	changed := With(ctx, "skills and memory v2")
	updated := Ensure(changed, old, read, makeMessage)
	require.Equal(t, old[:3], updated[:3])
	require.Equal(t, []message{makeMessage(Text(changed)), old[3]}, updated[3:])
	require.Equal(t, updated, Ensure(changed, updated, read, makeMessage))
	// Reverting to an older value still needs an update after the latest value.
	reverted := Ensure(ctx, updated, read, makeMessage)
	require.Equal(t, updated[:4], reverted[:4])
	require.Equal(t, Text(ctx), reverted[4].text)
	disabled := With(ctx, "")
	cleared := Ensure(disabled, updated, read, makeMessage)
	require.Equal(t, updated[:4], cleared[:4])
	require.Contains(t, cleared[4].text, "No skills or memory index")
	require.Equal(t, cleared, Ensure(disabled, cleared, read, makeMessage))
	require.Equal(t, old, Ensure(context.Background(), old, read, makeMessage))
	compacted := []message{makeMessage("summary")}
	require.Equal(t, compacted, Ensure(disabled, compacted, read, makeMessage))
	require.Equal(t, []message{makeMessage(Text(changed)), compacted[0]}, Ensure(changed, compacted, read, makeMessage))
	// A tool result is not a plain user request: append after the complete exchange.
	afterTool := Ensure(changed, old[:3], read, makeMessage)
	require.Equal(t, old[:3], afterTool[:3])
	require.Equal(t, Text(changed), afterTool[3].text)
	replay := []message{old[1], old[2]}
	require.Equal(t, append(replay, makeMessage(Text(changed))), SeedReplay(changed, old[:3], replay, read, makeMessage))
	require.Equal(t, replay, SeedReplay(changed, afterTool, replay, read, makeMessage))
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
