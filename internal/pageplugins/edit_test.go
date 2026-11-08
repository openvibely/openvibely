package pageplugins

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPagePublicationRejectsCrossProjectUpdates(t *testing.T) {
	m, _, project, other := newTestManager(t)
	first, err := m.PublishToInstance(t.Context(), replacementBundle(t, "1.0.0"), project, "", "test", "")
	require.NoError(t, err)
	files, _, err := m.EditWorkspace(t.Context(), first.Instance.ID)
	require.NoError(t, err)
	defer os.RemoveAll(files)
	require.NoError(t, os.WriteFile(filepath.Join(files, "index.html"), []byte("edited"), 0600))
	_, err = m.PublishEdit(t.Context(), first.Instance.ID, other, "test")
	require.ErrorIs(t, err, ErrNotFound)
	updated, err := m.PublishEdit(t.Context(), first.Instance.ID, project, "test")
	require.NoError(t, err)
	require.True(t, updated.Instance.Enabled)
	_, err = m.PublishEdit(t.Context(), first.Instance.ID, project, "test")
	require.NoError(t, err)
	require.NoError(t, m.Disable(t.Context(), first.Instance.ID))
	_, err = m.PublishEdit(t.Context(), first.Instance.ID, project, "test")
	require.ErrorIs(t, err, ErrDisabled)
}

func TestNativePagePublicationStartsWithoutApproval(t *testing.T) {
	m, _, project, _ := newTestManager(t)
	result, err := m.PublishToInstance(t.Context(), writeProcessPackage(t), project, "", "test", "")
	require.NoError(t, err)
	require.True(t, result.Instance.Enabled)
	require.False(t, result.ApprovalRequired)
	files, _, err := m.EditWorkspace(t.Context(), result.Instance.ID)
	require.NoError(t, err)
	defer os.RemoveAll(files)
	require.NoError(t, os.WriteFile(filepath.Join(files, "index.html"), []byte("new heading"), 0600))
	updated, err := m.PublishEdit(t.Context(), result.Instance.ID, project, "test")
	require.NoError(t, err)
	require.True(t, updated.Instance.Enabled)
	status, _ := m.ProcessStatus(updated.Instance.ID)
	require.Equal(t, "running", status)
}

func TestPageSourceFilesPersistAndAreInstanceOwned(t *testing.T) {
	m, store, project, other := newTestManager(t)
	first, err := m.PublishToInstance(t.Context(), replacementBundle(t, "1.0.0"), project, "", "test", "")
	require.NoError(t, err)
	second, err := m.CreateInstance(t.Context(), other, first.Instance.PackageID, first.Instance.PageID)
	require.NoError(t, err)
	require.NoError(t, m.Enable(t.Context(), second.ID))
	dir, hash, err := m.EditWorkspace(t.Context(), first.Instance.ID)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("unfinished edits"), 0600))
	restarted := NewManager(store.PagePlugins, m.Root())
	defer restarted.StopAll()
	sameDir, sameHash, err := restarted.EditWorkspace(t.Context(), first.Instance.ID)
	require.NoError(t, err)
	require.Equal(t, dir, sameDir)
	require.Equal(t, hash, sameHash)
	contents, err := os.ReadFile(filepath.Join(sameDir, "index.html"))
	require.NoError(t, err)
	require.Equal(t, "unfinished edits", string(contents))
	otherDir, _, err := restarted.EditWorkspace(t.Context(), second.ID)
	require.NoError(t, err)
	require.NotEqual(t, dir, otherDir)
	contents, err = os.ReadFile(filepath.Join(otherDir, "index.html"))
	require.NoError(t, err)
	require.NotEqual(t, "unfinished edits", string(contents))
	require.NoError(t, restarted.RemoveInstance(t.Context(), first.Instance.ID))
	_, err = os.Stat(dir)
	require.True(t, os.IsNotExist(err))
}

func TestTaskAndChatUseSamePageSources(t *testing.T) {
	m, store, project, _ := newTestManager(t)
	ctx := t.Context()
	first, err := m.PublishToInstance(ctx, replacementBundle(t, "1.0.0"), project, "", "test", "")
	require.NoError(t, err)
	dir, _, err := m.EditWorkspace(ctx, first.Instance.ID)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("chat edit"), 0600))
	_, err = m.PublishEdit(ctx, first.Instance.ID, project, "test")
	require.NoError(t, err)

	beforeDir, err := os.Stat(dir)
	require.NoError(t, err)
	// A task writes the permanent source files without swapping the folder or deleting other working files.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "obsolete.txt"), []byte("old"), 0600))
	updated, err := m.PublishToInstance(ctx, replacementBundle(t, "2.0.0"), project, "", "test", first.Instance.ID)
	require.NoError(t, err)
	contents, err := os.ReadFile(filepath.Join(dir, "index.html"))
	require.NoError(t, err)
	require.Contains(t, string(contents), "2.0.0")
	afterDir, err := os.Stat(dir)
	require.NoError(t, err)
	require.True(t, os.SameFile(beforeDir, afterDir), "publication must not move or replace the working directory")
	_, err = os.Stat(filepath.Join(dir, "obsolete.txt"))
	require.NoError(t, err)

	restarted := NewManager(store.PagePlugins, m.Root())
	defer restarted.StopAll()
	sameDir, currentHash, err := restarted.EditWorkspace(ctx, first.Instance.ID)
	require.NoError(t, err)
	require.Equal(t, dir, sameDir)
	require.Equal(t, updated.Instance.ContentHash, currentHash)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("chat edits task update"), 0600))
	result, err := restarted.PublishEdit(ctx, first.Instance.ID, project, "test")
	require.NoError(t, err)
	require.Equal(t, first.Instance.ID, result.Instance.ID)
	_, err = os.Stat(filepath.Join(filepath.Dir(dir), "base-hash"))
	require.True(t, os.IsNotExist(err))
}
