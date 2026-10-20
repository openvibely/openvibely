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

// WithSource resolves current permissions and cached indexes at each request boundary.
func WithSource(ctx context.Context, source func(context.Context) string) context.Context {
	return context.WithValue(ctx, key{}, source)
}
func Text(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	text, _ := ctx.Value(key{}).(string)
	if source, ok := ctx.Value(key{}).(func(context.Context) string); ok {
		text = source(ctx)
	}
	if text == "" {
		return ""
	}
	return Marker + "\n" + text + "\n</managed_knowledge_index>"
}

// Contains reports whether the visible conversation already carries an index.
func Contains[T any](items []T, read func(T) (string, string)) bool {
	for _, item := range items {
		role, content := read(item)
		if isIndex(role, content) {
			return true
		}
	}
	return false
}

// SeedReplay records an index update in the current turn's durable replay.
// visible is the assembled conversation before Ensure; replay contains this turn's
// current user input without attachment bytes. An index owned by earlier history
// is not copied into every turn. Placement matches Ensure, including multimodal input.
func SeedReplay[T, R any](ctx context.Context, visible []T, replay []R, read func(T) (string, string), makeMessage func(string) R) []R {
	text := updateText(ctx, visible, read)
	if text == "" {
		return replay
	}
	at := len(replay)
	if len(visible) > 0 && at > 0 {
		role, content := read(visible[len(visible)-1])
		if role == "user" && content != "" && !isIndex(role, content) {
			at--
		}
	}
	out := append([]R(nil), replay[:at]...)
	out = append(out, makeMessage(text))
	return append(out, replay[at:]...)
}

func isIndex(role, content string) bool {
	return role == "user" && strings.HasPrefix(content, Marker+"\n")
}

// updateText compares against the latest retained index, including disable notices.
func updateText[T any](ctx context.Context, items []T, read func(T) (string, string)) string {
	if ctx == nil || ctx.Value(key{}) == nil {
		return ""
	}
	text := Text(ctx)
	for i := len(items) - 1; i >= 0; i-- {
		role, content := read(items[i])
		if !isIndex(role, content) {
			continue
		}
		if text == "" {
			text = Marker + "\nNo skills or memory index is currently available. This supersedes previously listed indexes.\n</managed_knowledge_index>"
		}
		if content == text {
			return ""
		}
		return text
	}
	return text
}

// Ensure preserves existing history and inserts an update only when the latest
// index differs. Missing indexes are restored after compaction.
func Ensure[T any](ctx context.Context, items []T, read func(T) (string, string), makeMessage func(string) T) []T {
	text := updateText(ctx, items, read)
	if text == "" {
		return items
	}
	at := len(items)
	if at > 0 {
		role, content := read(items[at-1])
		if role == "user" && content != "" && !isIndex(role, content) {
			at--
		}
	}
	result := append([]T(nil), items[:at]...)
	result = append(result, makeMessage(text))
	return append(result, items[at:]...)
}
