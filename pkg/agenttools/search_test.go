package agenttools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchFilesPoliciesAndLimits(t *testing.T) {
	root := t.TempDir()
	for name, text := range map[string]string{"allowed.md": "needle one\nneedle two\nneedle three", "denied.md": "needle private", "other.txt": "needle excluded"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := WithFileReadAuthorization(context.Background(), func(_ context.Context, path string) bool { return filepath.Base(path) != "denied.md" })
	opts := SearchOptions{Include: "*.md", MaxResults: 2, MaxLineLength: 6, DisplayPath: filepath.Base}
	matches, err := SearchFiles(ctx, root, "needle", opts)
	if err != nil || len(matches) != 2 || matches[0] != "allowed.md:1: needle..." || matches[1] != "allowed.md:2: needle..." {
		t.Fatalf("matches=%q err=%v", matches, err)
	}
	opts.MaxResults = 0
	opts.MaxLineLength = 0
	matches, err = SearchFiles(ctx, root, "needle", opts)
	if err != nil || len(matches) != 3 || strings.Contains(strings.Join(matches, "\n"), "private") {
		t.Fatalf("matches=%q err=%v", matches, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := SearchFiles(cancelled, root, "needle", opts); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
	if _, err := SearchFiles(ctx, root, "[", opts); err == nil {
		t.Fatal("invalid regex accepted")
	}
}
