package openaiclient

import (
	"context"
	"github.com/openvibely/openvibely/pkg/attachmentcontext"
	"testing"
)

func TestAttachmentContextAfterNativeCompaction(t *testing.T) {
	ctx := attachmentcontext.With(context.Background(), "/uploads/report.docx")
	items := []any{
		map[string]any{"type": "message", "role": "user", "content": attachmentcontext.Text(ctx)},
		map[string]any{"type": "compaction", "encrypted_content": "opaque"},
		map[string]any{"type": "configuration_update"},
	}
	got := ensureAttachmentContext(ctx, items)
	if len(got) != 4 || got[2].(map[string]any)["content"] != attachmentcontext.Text(ctx) || got[3].(map[string]any)["type"] != "configuration_update" {
		t.Fatalf("%#v", got)
	}
	if len(ensureAttachmentContext(ctx, got)) != 4 {
		t.Fatal("duplicate context")
	}
}

func TestAttachmentContextReplacesStaleCheckpointVersion(t *testing.T) {
	source := attachmentcontext.NewSource(func(context.Context) (string, error) { return "/uploads/new.pdf", nil }, "openai-checkpoint-test")
	ctx := attachmentcontext.WithSource(context.Background(), source)
	stale := attachmentcontext.Wrap("/uploads/deleted.pdf")
	checkpoint := []any{
		map[string]any{"type": "message", "role": "user", "content": stale},
		map[string]any{"type": "message", "role": "user", "content": "do it"},
		map[string]any{"type": "function_call", "call_id": "c1", "name": "read"},
		map[string]any{"type": "function_call_output", "call_id": "c1", "output": "ok"},
		map[string]any{"type": "message", "role": "user", "content": stale},
	}
	got := ensureAttachmentContext(ctx, checkpoint)
	contexts := 0
	for i, item := range got {
		m := item.(map[string]any)
		if m["content"] == stale {
			t.Fatalf("stale context restored: %#v", got)
		}
		if m["content"] == attachmentcontext.Text(ctx) {
			contexts++
		}
		if m["type"] == "function_call" {
			if next := got[i+1].(map[string]any); next["type"] != "function_call_output" {
				t.Fatalf("tool pair split: %#v", got)
			}
		}
	}
	if contexts != 1 {
		t.Fatalf("contexts = %d: %#v", contexts, got)
	}
}
