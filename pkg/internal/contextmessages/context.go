// Package contextmessages shares context-update comparison and message placement.
// Callers own context resolution, formatting, and provider message conversions.
package contextmessages

// UpdateText compares text with the latest matching context. emptyText supersedes
// a retained context when text is empty; without retained context, nothing is added.
func UpdateText[T any](items []T, read func(T) (string, string), matches func(string, string) bool, text, emptyText string) string {
	for i := len(items) - 1; i >= 0; i-- {
		role, content := read(items[i])
		if !matches(role, content) {
			continue
		}
		if text == "" {
			text = emptyText
		}
		if content == text {
			return ""
		}
		return text
	}
	return text
}

// InsertUpdate inserts a nonempty update into items using the tail of visible to
// determine placement. For visible history, pass the same slice as both arguments;
// for durable replay, items contains only the current turn. A trailing plain user
// request follows the update. Adapters must not expose structured tool results as
// plain user text, so tool calls and their results remain together.
func InsertUpdate[T, R any](visible []T, items []R, read func(T) (string, string), matches func(string, string) bool, text string, makeMessage func(string) R) []R {
	if text == "" {
		return items
	}
	at := len(items)
	if len(visible) > 0 && at > 0 {
		role, content := read(visible[len(visible)-1])
		if role == "user" && content != "" && !matches(role, content) {
			at--
		}
	}
	out := make([]R, 0, len(items)+1)
	out = append(out, items[:at]...)
	out = append(out, makeMessage(text))
	return append(out, items[at:]...)
}
