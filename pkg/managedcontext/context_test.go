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
