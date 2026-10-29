package agenttools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSearchFilesUTF8Truncation(t *testing.T) {
	const maxLineLength = 200
	asciiPrefix := strings.Repeat("a", maxLineLength-1)
	longASCII := strings.Repeat("a", maxLineLength+1)
	tests := []struct {
		name     string
		line     []byte
		wantText string
	}{
		{
			name:     "three-byte rune crossing cutoff",
			line:     []byte(asciiPrefix + "界needle"),
			wantText: asciiPrefix + "...",
		},
		{
			name:     "four-byte rune crossing cutoff",
			line:     []byte(asciiPrefix + "🌱needle"),
			wantText: asciiPrefix + "...",
		},
		{
			name:     "long ASCII preserves excerpt length",
			line:     []byte(longASCII + "needle"),
			wantText: longASCII[:maxLineLength] + "...",
		},
		{
			name:     "short Unicode line is unchanged",
			line:     []byte("短い🌱 needle"),
			wantText: "短い🌱 needle",
		},
		{
			name:     "invalid UTF-8 is made valid",
			line:     []byte{0xff, 'n', 'e', 'e', 'd', 'l', 'e'},
			wantText: "\uFFFDneedle",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "match.txt"), tt.line, 0600); err != nil {
				t.Fatal(err)
			}

			matches, err := SearchFiles(context.Background(), root, "needle", SearchOptions{
				MaxLineLength: maxLineLength,
				DisplayPath:   filepath.Base,
			})
			if err != nil {
				t.Fatal(err)
			}
			want := "match.txt:1: " + tt.wantText
			if len(matches) != 1 || matches[0] != want {
				t.Fatalf("matches=%q, want [%q]", matches, want)
			}
			if !utf8.ValidString(matches[0]) {
				t.Fatalf("match is not valid UTF-8: %q", matches[0])
			}
		})
	}
}

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
