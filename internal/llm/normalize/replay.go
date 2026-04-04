package normalize

import "strings"

// NormalizeReplayOutputText performs lightweight cleanup for assistant replay
// snippets coming from execution history to reduce parser/provider incompatibilities.
func NormalizeReplayOutputText(text string) string {
	if text == "" {
		return text
	}
	out := NormalizeToolCallIDsInText(text)
	// Remove NUL bytes that can occasionally leak from stream parsers/log copies.
	out = strings.ReplaceAll(out, "\x00", "")
	return out
}
