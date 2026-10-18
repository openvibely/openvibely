package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/memory"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestMaintenanceRejectsConcurrentTaskMemoryChanges(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	task := memory.RuntimeTools(repo, true, true)
	taskCall := func(name string, args map[string]string) string {
		t.Helper()
		data, err := json.Marshal(args)
		require.NoError(t, err)
		out, handled, failed, err := task.Executor(ctx, name, data)
		require.NoError(t, err)
		require.True(t, handled)
		require.False(t, failed, out)
		return out
	}
	save := func(handle, content string) {
		taskCall("memory_save", map[string]string{"handle": handle, "content": content, "summary": "durable fact"})
	}
	save("existing.md", "original")
	_, maintenance, err := buildScopedFilesRuntimeTools(ctx, "project", repo, models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{Directory: ".openvibely/memories", Permissions: []string{"read", "write", "delete"}}}})
	require.NoError(t, err)
	call := func(name string, args map[string]string, reject bool) string {
		t.Helper()
		data, err := json.Marshal(args)
		require.NoError(t, err)
		out, handled, failed, err := maintenance.Executor(ctx, name, data)
		require.True(t, handled)
		require.Equal(t, reject, failed, out)
		if reject {
			require.ErrorContains(t, err, "read_file")
		} else {
			require.NoError(t, err)
		}
		return out
	}
	call("read_file", map[string]string{"file_path": memory.IndexFileName}, false)
	save("new.md", "new fact")
	call("write_file", map[string]string{"file_path": memory.IndexFileName, "content": "- existing.md — old index"}, true)
	require.Contains(t, taskCall("memory_view", map[string]string{"handle": "new.md"}), "new fact")
	call("read_file", map[string]string{"file_path": memory.IndexFileName}, false)
	call("write_file", map[string]string{"file_path": memory.IndexFileName, "content": "- existing.md — existing\n- new.md — new fact\n"}, false)
	for _, operation := range []string{"edit_file", "delete_file", "write_file"} {
		call("read_file", map[string]string{"file_path": "existing.md"}, false)
		save("existing.md", "corrected "+operation)
		call(operation, map[string]string{"file_path": "existing.md", "content": "stale", "old_string": "corrected", "new_string": "stale"}, true)
		require.Contains(t, taskCall("memory_view", map[string]string{"handle": "existing.md"}), "corrected "+operation)
	}
	call("read_file", map[string]string{"file_path": "existing.md"}, false)
	taskCall("memory_delete", map[string]string{"handle": "existing.md"})
	call("write_file", map[string]string{"file_path": "existing.md", "content": "resurrected stale fact"}, true)
	// Blind overwrites of existing files are also rejected; new files still work.
	_, other, err := buildScopedFilesRuntimeTools(ctx, "project", repo, models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{Directory: ".openvibely/memories", Permissions: []string{"read", "write", "delete"}}}})
	require.NoError(t, err)
	_, _, failed, err := other.Executor(ctx, "write_file", json.RawMessage(`{"file_path":"new.md","content":"blind"}`))
	require.True(t, failed)
	require.Error(t, err)
	call("write_file", map[string]string{"file_path": "maintenance.md", "content": "fresh"}, false)
	call("edit_file", map[string]string{"file_path": "maintenance.md", "old_string": "fresh", "new_string": "updated"}, false)
	call("delete_file", map[string]string{"file_path": "maintenance.md"}, false)
}
