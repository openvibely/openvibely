package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestInspectRootVisionSourceStates(t *testing.T) {
	t.Parallel()

	status := InspectRootVisionSource("")
	require.Equal(t, visionSourceNoRepo, status.State)
	require.Equal(t, visionSourceNoRepoMessage, status.Message)

	root := t.TempDir()
	missing := InspectRootVisionSource(root)
	require.Equal(t, visionSourceMissing, missing.State)
	require.Equal(t, visionSourceMissingMessage, missing.Message)

	require.NoError(t, os.WriteFile(filepath.Join(root, rootVisionSourceName), []byte("# Direction\n"), 0o600))
	found := InspectRootVisionSource(root)
	require.Equal(t, visionSourceFound, found.State)
	require.Equal(t, visionSourceFoundMessage, found.Message)

	dirRoot := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dirRoot, rootVisionSourceName), 0o700))
	unreadable := InspectRootVisionSource(dirRoot)
	require.Equal(t, visionSourceUnreadable, unreadable.State)
	require.Equal(t, visionSourceUnreadableMessage, unreadable.Message)
}

func TestInspectRootVisionSourceRejectsUnreadableRegularFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, rootVisionSourceName)
	require.NoError(t, os.WriteFile(path, []byte("vision"), 0o600))
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	file, err := os.Open(path)
	if err == nil {
		_ = file.Close()
		t.Skip("test process can read mode-000 files")
	}
	status := InspectRootVisionSource(root)
	require.Equal(t, visionSourceUnreadable, status.State)
	require.Equal(t, visionSourceUnreadableMessage, status.Message)
}

func TestInspectRootVisionSourceBoundsReads(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, rootVisionSourceName)
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	require.NoError(t, os.Truncate(path, maxRootVisionSourceBytes+1))

	status := InspectRootVisionSource(root)
	require.Equal(t, visionSourceTruncated, status.State)
	require.Equal(t, visionSourceTruncatedMessage, status.Message)
}

func TestAnnotateMaintainedSDLCVisionSourceWarnsWhenMissing(t *testing.T) {
	t.Parallel()

	candidate := models.AutomationDraftCandidate{
		AdapterKey: AutomationAdapterNativeSDLC,
		Nodes:      []models.AutomationDraftNode{{Key: maintainedVisionSuggestionsNodeKey, Name: "Vision Suggestions"}},
		Warnings:   []string{"keep this warning", visionSourceFoundMessage},
	}
	got := AnnotateMaintainedSDLCVisionSource(candidate, t.TempDir())
	require.Equal(t, []string{"keep this warning", visionSourceMissingMessage}, got.Warnings)
	require.Empty(t, got.Assumptions)

	custom := models.AutomationDraftCandidate{
		AdapterKey: "custom",
		Nodes:      []models.AutomationDraftNode{{Key: maintainedVisionSuggestionsNodeKey}},
	}
	unchanged := AnnotateMaintainedSDLCVisionSource(custom, "")
	require.Empty(t, unchanged.Warnings)
	require.Empty(t, unchanged.Assumptions)
}

func TestAnnotateMaintainedSDLCVisionSourceFoundAndGitHub(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, rootVisionSourceName), []byte("vision"), 0o600))
	candidate := models.AutomationDraftCandidate{
		AdapterKey:  AutomationAdapterGitHubSDLC,
		Nodes:       []models.AutomationDraftNode{{Key: maintainedVisionSuggestionsNodeKey}},
		Assumptions: []string{"keep this assumption"},
		Warnings:    []string{visionSourceMissingMessage},
	}
	got := AnnotateMaintainedSDLCVisionSource(candidate, root)
	require.Equal(t, []string{"keep this assumption", visionSourceFoundMessage}, got.Assumptions)
	require.Empty(t, got.Warnings)
}
