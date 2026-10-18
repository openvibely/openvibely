package agenttools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/pkg/agenttools"
)

// BenchmarkGrepSearchFixture compares the old and streaming implementations
// on the same generated 105 MiB fixture (darwin/arm64, Apple M5 Pro). The old
// implementation was measured before the change; the streaming implementation
// was measured three times afterward. The reported after values are medians.
//
// Results (ns/op, B/op, allocs/op):
//   NoMatch:      before 55,762,917; 242,823,360; 21,684
//                 after  38,906,541;     765,352; 10,561
//   PreCancelled: before 52,774,333; 242,466,896; 21,606
//                 after       4,125;           0;      0
//
// Run with: go test ./pkg/agenttools -run '^$' -bench '^BenchmarkGrepSearchFixture$' -benchmem -benchtime=1x -count=3
func BenchmarkGrepSearchFixture(b *testing.B) {
	root := b.TempDir()
	const fileCount = 1500
	const fileSize = 72 * 1024
	line := strings.Repeat("a", 1023) + "\n"
	payload := []byte(strings.Repeat(line, fileSize/len(line)))
	if len(payload) < fileSize {
		payload = append(payload, []byte(strings.Repeat("a", fileSize-len(payload)))...)
	}
	for i := 0; i < fileCount; i++ {
		path := filepath.Join(root, fmt.Sprintf("text-%04d.txt", i))
		if err := os.WriteFile(path, payload, 0o644); err != nil {
			b.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte("no match\x00"+strings.Repeat("b", fileSize)), 0o644); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary.txt"), []byte("no match\x00"+strings.Repeat("b", fileSize)), 0o644); err != nil {
		b.Fatal(err)
	}

	input := json.RawMessage(`{"pattern":"zzNoSuchTokenQq"}`)
	b.Run("NoMatch", func(b *testing.B) {
		ctx := context.Background()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := agenttools.Execute(ctx, root, "grep_search", input); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("PreCancelled", func(b *testing.B) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _ = agenttools.Execute(ctx, root, "grep_search", input)
		}
	})
}
