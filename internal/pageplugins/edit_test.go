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
	files, err := m.EditWorkspace(t.Context(), first.Instance.ID)
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
	files, err := m.EditWorkspace(t.Context(), result.Instance.ID)
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
	dir, err := m.EditWorkspace(t.Context(), first.Instance.ID)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("unfinished edits"), 0600))
	restarted := NewManager(store.PagePlugins, m.Root())
	defer restarted.StopAll()
	sameDir, err := restarted.EditWorkspace(t.Context(), first.Instance.ID)
	require.NoError(t, err)
	require.Equal(t, dir, sameDir)
	contents, err := os.ReadFile(filepath.Join(sameDir, "index.html"))
	require.NoError(t, err)
	require.Equal(t, "unfinished edits", string(contents))
	otherDir, err := restarted.EditWorkspace(t.Context(), second.ID)
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
	dir, err := m.EditWorkspace(ctx, first.Instance.ID)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("chat edit"), 0600))
	_, err = m.PublishEdit(ctx, first.Instance.ID, project, "test")
	require.NoError(t, err)

	beforeDir, err := os.Stat(dir)
	require.NoError(t, err)
	// A task writes the permanent source files without swapping the folder or deleting other working files.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "obsolete.txt"), []byte("old"), 0600))
	_, err = m.PublishToInstance(ctx, replacementBundle(t, "2.0.0"), project, "", "test", first.Instance.ID)
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
	sameDir, err := restarted.EditWorkspace(ctx, first.Instance.ID)
	require.NoError(t, err)
	require.Equal(t, dir, sameDir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("chat edits task update"), 0600))
	result, err := restarted.PublishEdit(ctx, first.Instance.ID, project, "test")
	require.NoError(t, err)
	require.Equal(t, first.Instance.ID, result.Instance.ID)
	_, err = os.Stat(filepath.Join(filepath.Dir(dir), "base-hash"))
	require.True(t, os.IsNotExist(err))
}

func TestRemoveEditedPageCleansPackageAndPreservesSharedSources(t *testing.T) {
	for _, shared := range []bool{false, true} {
		name := "unshared"
		if shared {
			name = "shared"
		}
		t.Run(name, func(t *testing.T) {
			m, _, project, other := newTestManager(t)
			ctx := t.Context()
			first, err := m.PublishToInstance(ctx, replacementBundle(t, "1.0.0"), project, "", "test", "")
			require.NoError(t, err)
			dir, err := m.EditWorkspace(ctx, first.Instance.ID)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("edited page"), 0600))
			edited, err := m.PublishEdit(ctx, first.Instance.ID, project, "test")
			require.NoError(t, err)
			var secondID string
			if shared {
				second, err := m.CreateInstance(ctx, other, edited.Instance.PackageID, edited.Instance.PageID)
				require.NoError(t, err)
				secondID = second.ID
			}
			require.NoError(t, m.RemoveInstance(ctx, first.Instance.ID))
			if shared {
				// The second page has never loaded: enabling must still be able to
				// initialize from the retained package after the owner is deleted.
				require.NoError(t, m.Enable(ctx, secondID))
				secondDir, err := m.EditWorkspace(ctx, secondID)
				require.NoError(t, err)
				body, err := os.ReadFile(filepath.Join(secondDir, "index.html"))
				require.NoError(t, err)
				require.Equal(t, "edited page", string(body))
				require.NoError(t, m.RemoveInstance(ctx, secondID))
			}
			pkg, err := m.store.GetPackage(ctx, edited.Instance.PackageID)
			require.NoError(t, err)
			require.Nil(t, pkg)
			_, err = os.Stat(dir)
			require.True(t, os.IsNotExist(err))
		})
	}
}

func TestTaskPublicationReplacesSourcePathTypes(t *testing.T) {
	for _, oldDirectory := range []bool{false, true} {
		name := "file-to-directory"
		if oldDirectory {
			name = "directory-to-file"
		}
		t.Run(name, func(t *testing.T) {
			m, _, project, _ := newTestManager(t)
			writeAsset := func(root string, directory bool, content string) {
				t.Helper()
				path := filepath.Join(root, "assets")
				if directory {
					require.NoError(t, os.MkdirAll(path, 0700))
					path = filepath.Join(path, "app.js")
				}
				require.NoError(t, os.WriteFile(path, []byte(content), 0600))
			}
			first := replacementBundle(t, "1.0.0")
			writeAsset(first, oldDirectory, "old")
			result, err := m.PublishToInstance(t.Context(), first, project, "", "test", "")
			require.NoError(t, err)
			next := replacementBundle(t, "1.0.1")
			writeAsset(next, !oldDirectory, "new")
			_, err = m.PublishToInstance(t.Context(), next, project, "", "test", result.Instance.ID)
			require.NoError(t, err)
			path := filepath.Join(m.pageSourceDir(result.Instance.ID), "assets")
			if !oldDirectory {
				path = filepath.Join(path, "app.js")
			}
			content, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, "new", string(content))
		})
	}
}
