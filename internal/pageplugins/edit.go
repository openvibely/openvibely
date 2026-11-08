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

// EditWorkspace returns durable, instance-owned source files. Installed packages may
// be shared between projects, so edits never mutate the shared package in place.
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
	root := filepath.Join(m.root, "page-sources", in.ID)
	dir := filepath.Join(root, "files")
	stamp := filepath.Join(root, "base-hash")
	if hash, err := os.ReadFile(stamp); err == nil {
		return dir, string(hash), nil
	} else if !os.IsNotExist(err) {
		return "", "", err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", "", err
	}
	// Initialize atomically; an interrupted copy must never become the working tree.
	staging, err := os.MkdirTemp(root, ".init-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(staging)
	if err = copyTree(os.DirFS(pkg.InstallPath), staging); err != nil {
		return "", "", err
	}
	if _, err = os.Stat(dir); os.IsNotExist(err) {
		if err = os.Rename(staging, dir); err != nil {
			return "", "", err
		}
	} else if err != nil {
		return "", "", err
	}
	if err = os.WriteFile(stamp, []byte(pkg.ContentHash), 0600); err != nil {
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
	result, err := m.publishLocked(ctx, source, projectID, in.PageID, by, instanceID)
	if err != nil {
		return nil, err
	}
	if source == filepath.Join(m.root, "page-sources", in.ID, "files") {
		if err := os.WriteFile(filepath.Join(m.root, "page-sources", in.ID, "base-hash"), []byte(result.Instance.ContentHash), 0600); err != nil {
			return nil, err
		}
	}
	return result, nil
}
