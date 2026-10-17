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

// SeedReplay records a first index insertion in the current turn's durable replay.
// visible is the assembled conversation before Ensure; replay contains this turn's
// current user input without attachment bytes. An index owned by earlier history
// is not copied into every turn. Placement matches Ensure, including multimodal input.
func SeedReplay[T, R any](ctx context.Context, visible []T, replay []R, read func(T) (string, string), makeMessage func(string) R) []R {
	text := Text(ctx)
	if text == "" || Contains(visible, read) {
		return replay
	}
	at := len(replay)
	if len(visible) > 0 && at > 0 {
		role, content := read(visible[len(visible)-1])
		if role == "user" && content != "" {
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

// Ensure keeps an identical index at its original position. Changed indexes replace
// the first old index in place; missing indexes are restored, and duplicates removed.
// An empty current index removes old indexes. Structured tool exchanges are untouched.
// Call after rebuilding history and after compaction; individual skill bodies are
// deliberately reloaded by the model rather than permanently pinned in context.
func Ensure[T any](ctx context.Context, items []T, read func(T) (string, string), makeMessage func(string) T) []T {
	if ctx == nil || ctx.Value(key{}) == nil {
		return items
	}
	text := Text(ctx)
	keep, first := -1, -1
	for i, item := range items {
		role, content := read(item)
		if !isIndex(role, content) {
			continue
		}
		if first < 0 {
			first = i
		}
		if keep < 0 && content == text {
			keep = i
		}
	}
	if keep < 0 {
		keep = first
	}
	out := make([]T, 0, len(items)+1)
	for i, item := range items {
		role, content := read(item)
		if isIndex(role, content) {
			if i != keep || text == "" {
				continue
			}
			if content != text {
				item = makeMessage(text)
			}
		}
		out = append(out, item)
	}
	if text == "" || keep >= 0 {
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
