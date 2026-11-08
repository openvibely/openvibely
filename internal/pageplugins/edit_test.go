package pageplugins

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPageDraftRejectsStaleAndCrossProjectUpdates(t *testing.T) {
	m, _, project, other := newTestManager(t)
	first, err := m.PublishToInstance(t.Context(), replacementBundle(t, "1.0.0"), project, "", "test", "")
	require.NoError(t, err)
	draft, hash, err := m.EditWorkspace(t.Context(), first.Instance.ID)
	require.NoError(t, err)
	defer os.RemoveAll(draft)
	require.NoError(t, os.WriteFile(filepath.Join(draft, "index.html"), []byte("edited"), 0600))
	_, err = m.ApplyEdit(t.Context(), first.Instance.ID, other, hash, draft, "test")
	require.ErrorIs(t, err, ErrNotFound)
	updated, err := m.ApplyEdit(t.Context(), first.Instance.ID, project, hash, draft, "test")
	require.NoError(t, err)
	require.True(t, updated.Instance.Enabled)
	_, err = m.ApplyEdit(t.Context(), first.Instance.ID, project, hash, draft, "test")
	require.ErrorContains(t, err, "page changed while editing")
	require.NoError(t, m.Disable(t.Context(), first.Instance.ID))
	_, err = m.ApplyEdit(t.Context(), first.Instance.ID, project, updated.Instance.ContentHash, draft, "test")
	require.ErrorIs(t, err, ErrDisabled)
}

func TestNativePageDraftStartsWithoutApproval(t *testing.T) {
	m, _, project, _ := newTestManager(t)
	result, err := m.PublishToInstance(t.Context(), writeProcessPackage(t), project, "", "test", "")
	require.NoError(t, err)
	require.True(t, result.Instance.Enabled)
	require.False(t, result.ApprovalRequired)
	draft, hash, err := m.EditWorkspace(t.Context(), result.Instance.ID)
	require.NoError(t, err)
	defer os.RemoveAll(draft)
	require.NoError(t, os.WriteFile(filepath.Join(draft, "index.html"), []byte("new heading"), 0600))
	updated, err := m.ApplyEdit(t.Context(), result.Instance.ID, project, hash, draft, "test")
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
	dir, hash, err := m.EditWorkspace(ctx, first.Instance.ID)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("chat edit"), 0600))
	_, err = m.ApplyEdit(ctx, first.Instance.ID, project, hash, dir, "test")
	require.NoError(t, err)

	// A task replaces the authoritative source, including removing old files.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "obsolete.txt"), []byte("old"), 0600))
	updated, err := m.PublishToInstance(ctx, replacementBundle(t, "2.0.0"), project, "", "test", first.Instance.ID)
	require.NoError(t, err)
	contents, err := os.ReadFile(filepath.Join(dir, "index.html"))
	require.NoError(t, err)
	require.Contains(t, string(contents), "2.0.0")
	_, err = os.Stat(filepath.Join(dir, "obsolete.txt"))
	require.True(t, os.IsNotExist(err))

	restarted := NewManager(store.PagePlugins, m.Root())
	defer restarted.StopAll()
	sameDir, currentHash, err := restarted.EditWorkspace(ctx, first.Instance.ID)
	require.NoError(t, err)
	require.Equal(t, dir, sameDir)
	require.Equal(t, updated.Instance.ContentHash, currentHash)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("chat edits task update"), 0600))
	result, err := restarted.ApplyEdit(ctx, first.Instance.ID, project, currentHash, dir, "test")
	require.NoError(t, err)
	require.Equal(t, first.Instance.ID, result.Instance.ID)
	_, err = os.Stat(filepath.Join(filepath.Dir(dir), "base-hash"))
	require.True(t, os.IsNotExist(err))
}

func TestLegacyStalePageSourcesRecoverWithoutDeletingEdits(t *testing.T) {
	m, _, project, _ := newTestManager(t)
	ctx := t.Context()
	first, err := m.PublishToInstance(ctx, replacementBundle(t, "2.0.0"), project, "", "test", "")
	require.NoError(t, err)
	dir := m.pageSourceDir(first.Instance.ID)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("unfinished old edit"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(dir), "base-hash"), []byte("old-version-hash"), 0600))
	_, hash, err := m.EditWorkspace(ctx, first.Instance.ID)
	require.NoError(t, err)
	require.Equal(t, first.Instance.ContentHash, hash)
	contents, err := os.ReadFile(filepath.Join(dir, "index.html"))
	require.NoError(t, err)
	require.Contains(t, string(contents), "2.0.0")
	archives, err := filepath.Glob(filepath.Join(filepath.Dir(dir), "recovered-edits-*", "index.html"))
	require.NoError(t, err)
	require.Len(t, archives, 1)
	contents, err = os.ReadFile(archives[0])
	require.NoError(t, err)
	require.Equal(t, "unfinished old edit", string(contents))
}
