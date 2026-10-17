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
