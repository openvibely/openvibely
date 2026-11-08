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
