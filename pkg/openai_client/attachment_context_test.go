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

func TestAttachmentContextAppendsAfterStaleCheckpointVersion(t *testing.T) {
	source := attachmentcontext.NewSource(func(context.Context) (string, error) { return "/uploads/new.pdf", nil }, "openai-checkpoint-test")
	ctx := attachmentcontext.WithSource(context.Background(), source)
	stale := attachmentcontext.Wrap("/uploads/deleted.pdf")
	checkpoint := []any{
		map[string]any{"type": "message", "role": "user", "content": stale},
		map[string]any{"type": "message", "role": "user", "content": "do it"},
		map[string]any{"type": "function_call", "call_id": "c1", "name": "read"},
		map[string]any{"type": "function_call_output", "call_id": "c1", "output": "ok"},
	}
	got := ensureAttachmentContext(ctx, checkpoint)
	if len(got) != 5 {
		t.Fatalf("%#v", got)
	}
	for i := range checkpoint {
		if got[i].(map[string]any)["content"] != checkpoint[i].(map[string]any)["content"] {
			t.Fatalf("checkpoint prefix rewritten: %#v", got)
		}
	}
	if got[4].(map[string]any)["content"] != attachmentcontext.Text(ctx) {
		t.Fatalf("current context not appended after tool pair: %#v", got)
	}
}
