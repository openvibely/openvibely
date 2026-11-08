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
	dir := m.pageSourceDir(in.ID)
	// Migrate the former side-chat workspace once. If an external update
	// already made it stale, keep its unfinished files as a recovery archive
	// and use the current page. New publications no longer create this marker.
	stamp := filepath.Join(filepath.Dir(dir), "base-hash")
	if previous, readErr := os.ReadFile(stamp); readErr == nil {
		if string(previous) != pkg.ContentHash {
			archive, err := os.MkdirTemp(filepath.Dir(dir), "recovered-edits-")
			if err != nil {
				return "", "", err
			}
			if err := copyTree(os.DirFS(dir), archive); err != nil {
				return "", "", err
			}
			if err := m.replacePageSources(in.ID, pkg.InstallPath); err != nil {
				return "", "", err
			}
		}
		if err := os.Remove(stamp); err != nil && !os.IsNotExist(err) {
			return "", "", err
		}
	} else if !os.IsNotExist(readErr) {
		return "", "", readErr
	}
	if _, err := os.Stat(dir); err == nil {
		return dir, pkg.ContentHash, nil
	} else if !os.IsNotExist(err) {
		return "", "", err
	}
	if err := m.replacePageSources(in.ID, pkg.InstallPath); err != nil {
		return "", "", err
	}
	return dir, pkg.ContentHash, nil
}

// ApplyEdit rejects concurrent publication and publishes a new revision without changing
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

	return result, nil
}

// pageSourceDir is the single mutable source tree for an instance. Installed
// packages are immutable runtime snapshots, not a second editing workspace.
func (m *Manager) pageSourceDir(instanceID string) string {
	return filepath.Join(m.root, "page-sources", instanceID, "files")
}

// replacePageSources imports a task publication into the same files used by chat.
// Callers hold publishMu. Stage first so a copy failure leaves existing files intact.
func (m *Manager) replacePageSources(instanceID, source string) error {
	dir := m.pageSourceDir(instanceID)
	if filepath.Clean(source) == filepath.Clean(dir) {
		return nil
	}
	root := filepath.Dir(dir)
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(root, ".import-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := copyTree(os.DirFS(source), staging); err != nil {
		return err
	}
	// A prior interrupted import can leave the previous tree here. It is only
	// removed once a complete replacement has been staged.
	backup := filepath.Join(root, ".previous")
	if err := os.RemoveAll(backup); err != nil {
		return err
	}
	hadPrevious := false
	if err := os.Rename(dir, backup); err == nil {
		hadPrevious = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(staging, dir); err != nil {
		if hadPrevious {
			_ = os.Rename(backup, dir)
		}
		return err
	}
	_ = os.RemoveAll(backup)
	// Older releases recorded a separate editing revision. There is now only
	// the instance's installed revision and its authoritative source tree.
	_ = os.Remove(filepath.Join(root, "base-hash"))
	return nil
}
