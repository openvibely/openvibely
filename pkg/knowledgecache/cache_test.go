package knowledgecache

import (
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExternalChangesAndIsolation(t *testing.T) {
	c, err := New(30 * time.Millisecond)
	require.NoError(t, err)
	defer c.Close()
	root := t.TempDir()
	other := t.TempDir()
	path := filepath.Join(root, "MEMORIES.md")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0600))
	builds := 0
	get := func() string {
		return c.Get("project-a", []string{root}, func() any { builds++; b, _ := os.ReadFile(path); return string(b) }).(string)
	}
	require.Equal(t, "old", get())
	require.Equal(t, "old", get())
	require.Equal(t, 1, builds)
	otherBuilds := 0
	getOther := func() any {
		return c.Get("project-b", []string{other}, func() any { otherBuilds++; return "isolated" })
	}
	getOther()
	tmp := filepath.Join(root, "replacement")
	require.NoError(t, os.WriteFile(tmp, []byte("new"), 0600))
	require.NoError(t, os.Rename(tmp, path))
	require.Eventually(t, func() bool { return get() == "new" }, 3*time.Second, 10*time.Millisecond)
	getOther()
	require.Equal(t, 1, otherBuilds)
	require.NoError(t, os.Remove(path))
	require.Eventually(t, func() bool { return get() == "" }, 3*time.Second, 10*time.Millisecond)
	nested := filepath.Join(root, "new", "nested")
	require.NoError(t, os.MkdirAll(nested, 0700))
	require.Eventually(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.watched[nested] }, 3*time.Second, 10*time.Millisecond)
}
func TestImmediateInvalidationAndAbsentRoot(t *testing.T) {
	c, err := New(time.Hour)
	require.NoError(t, err)
	defer c.Close()
	root := filepath.Join(t.TempDir(), "memories")
	builds := 0
	get := func() any { return c.Get("project", []string{root}, func() any { builds++; return builds }) }
	require.Equal(t, 1, get())
	require.Equal(t, 1, get())
	require.NoError(t, os.MkdirAll(root, 0700))
	c.Invalidate(root)
	require.Equal(t, 2, get())
	require.Equal(t, 2, get())
}
