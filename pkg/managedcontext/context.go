// Package managedcontext keeps the current permitted knowledge index in conversation messages.
package managedcontext

import (
	"context"
	"strings"
)

type key struct{}

const Marker = "<managed_knowledge_index>"

func With(ctx context.Context, index string) context.Context {
	return context.WithValue(ctx, key{}, index)
}
func Text(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	text, _ := ctx.Value(key{}).(string)
	if text == "" {
		return ""
	}
	return Marker + "\n" + text + "\n</managed_knowledge_index>"
}

// Ensure replaces old indexes, preserving structured tool exchanges and the latest request.
// Call after rebuilding history and after compaction; missing individual skill bodies
// are deliberately reloaded by the model rather than permanently pinned in context.
func Ensure[T any](ctx context.Context, items []T, read func(T) (string, string), makeMessage func(string) T) []T {
	if ctx == nil || ctx.Value(key{}) == nil {
		return items
	}
	text := Text(ctx)
	out := make([]T, 0, len(items)+1)
	for _, item := range items {
		role, content := read(item)
		if role == "user" && strings.HasPrefix(content, Marker+"\n") {
			continue
		}
		out = append(out, item)
	}
	if text == "" {
		return out
	}
	at := len(out)
	if at > 0 {
		role, content := read(out[at-1])
		if role == "user" && content != "" {
			at--
		}
	}
	result := append([]T(nil), out[:at]...)
	result = append(result, makeMessage(text))
	return append(result, out[at:]...)
}
