package pageplugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EditWorkspace copies the installed bundle, never handing an editor the live files.
func (m *Manager) EditWorkspace(ctx context.Context, instanceID string) (string, string, error) {
	m.publishMu.Lock()
	defer m.publishMu.Unlock()
	in, pkg, _, err := m.load(ctx, instanceID)
	if err != nil {
		return "", "", err
	}
	if !in.Enabled {
		return "", "", ErrDisabled
	}
	dir, err := os.MkdirTemp("", "ov-page-edit-")
	if err != nil {
		return "", "", err
	}
	if err = copyTree(os.DirFS(pkg.InstallPath), dir); err != nil {
		os.RemoveAll(dir)
		return "", "", err
	}
	return dir, pkg.ContentHash, nil
}

// ApplyEdit rejects stale drafts and publishes a new revision without changing
// the page identity, saved data, or conversation. Startup failure rolls back.
func (m *Manager) ApplyEdit(ctx context.Context, instanceID, projectID, baseHash, source, by string) (*PublishResult, error) {
	m.publishMu.Lock()
	defer m.publishMu.Unlock()
	in, _, current, err := m.load(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	if in.ProjectID != projectID {
		return nil, ErrNotFound
	}
	if !in.Enabled {
		return nil, ErrDisabled
	}
	if in.ContentHash != baseHash {
		return nil, fmt.Errorf("page changed while editing; start from the current page before applying changes")
	}
	raw, err := os.ReadFile(filepath.Join(source, ManifestFile))
	if err != nil {
		return nil, err
	}
	man, err := ParseManifest(raw)
	if err != nil {
		return nil, err
	}
	if man.ID != current.ID {
		return nil, fmt.Errorf("page edits must preserve the package ID")
	}
	if _, ok := man.Page(in.PageID); !ok {
		return nil, fmt.Errorf("page edits must preserve the page ID")
	}
	// Versioning is an implementation detail; the user does not manage it.
	man.Version = strings.Split(current.Version, "-")[0] + fmt.Sprintf("-edit.%d", time.Now().UnixNano())
	raw, err = json.MarshalIndent(man, "", "  ")
	if err != nil {
		return nil, err
	}
	if err = os.WriteFile(filepath.Join(source, ManifestFile), raw, 0644); err != nil {
		return nil, err
	}
	return m.publishLocked(ctx, source, projectID, in.PageID, by, instanceID)
}
