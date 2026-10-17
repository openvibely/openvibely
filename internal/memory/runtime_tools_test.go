package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConcurrentMemorySavesRetainIndexEntries(t *testing.T) {
	root := t.TempDir()
	rt := RuntimeTools(root, true, true)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			input, _ := json.Marshal(map[string]string{"handle": fmt.Sprintf("topic-%d.md", i), "summary": "Durable topic", "content": "Reusable fact"})
			out, handled, isErr, err := rt.Executor(context.Background(), "memory_save", input)
			if err != nil || !handled || isErr {
				t.Errorf("save failed: %s %v", out, err)
			}
		}(i)
	}
	wg.Wait()
	dir, err := SharedRepoMemoryDir(root)
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(dir, IndexFileName))
	require.NoError(t, err)
	require.Len(t, IndexedMemoryHandles(string(raw)), 16)
}

func TestMemoryRuntimeRejectsTraversalAndSymlinkEscapes(t *testing.T) {
	root := t.TempDir()
	rt := RuntimeTools(root, true, true)
	dir, err := SharedRepoMemoryDir(root)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0700))
	outside := filepath.Join(t.TempDir(), "outside.md")
	require.NoError(t, os.WriteFile(outside, []byte("private outside fact"), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "escape.md")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, IndexFileName), []byte("- escape.md — outside\n"), 0600))
	for _, handle := range []string{"escape.md", "../outside.md", outside, "MEMORIES.md"} {
		for _, tool := range []string{"memory_view", "memory_save", "memory_delete"} {
			input, _ := json.Marshal(map[string]string{"handle": handle, "summary": "Changed", "content": "Changed"})
			out, handled, isErr, err := rt.Executor(context.Background(), tool, input)
			require.NoError(t, err)
			require.True(t, handled)
			require.True(t, isErr, "%s %s: %s", tool, handle, out)
		}
	}
	raw, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "private outside fact", string(raw))
}

func TestMemoryRuntimeUpdatesAndDeletesExistingNestedTopics(t *testing.T) {
	rt := RuntimeTools(t.TempDir(), true, true)
	for _, content := range []string{"Original preference", "Corrected preference"} {
		input, _ := json.Marshal(map[string]string{"handle": "preferences/output.md", "content": content, "summary": "Output preference"})
		_, handled, isErr, err := rt.Executor(context.Background(), "memory_save", input)
		require.NoError(t, err)
		require.True(t, handled)
		require.False(t, isErr)
		out, _, isErr, err := rt.Executor(context.Background(), "memory_view", json.RawMessage(`{"handle":"preferences/output.md"}`))
		require.NoError(t, err)
		require.False(t, isErr)
		require.Contains(t, out, content)
	}
	_, _, isErr, err := rt.Executor(context.Background(), "memory_delete", json.RawMessage(`{"handle":"preferences/output.md"}`))
	require.NoError(t, err)
	require.False(t, isErr)
	_, _, isErr, err = rt.Executor(context.Background(), "memory_view", json.RawMessage(`{"handle":"preferences/output.md"}`))
	require.NoError(t, err)
	require.True(t, isErr)
}
