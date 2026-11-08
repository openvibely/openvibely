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

// EditWorkspace returns the durable, instance-owned files used by both coding
// tools and the page runtime.
func (m *Manager) EditWorkspace(ctx context.Context, instanceID string) (string, error) {
	m.publishMu.Lock()
	defer m.publishMu.Unlock()
	in, pkg, _, err := m.load(ctx, instanceID)
	if err != nil {
		return "", err
	}
	if !in.Enabled {
		return "", ErrDisabled
	}
	dir := m.pageSourceDir(in.ID)
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := m.replacePageSources(in.ID, pkg.InstallPath); err != nil {
		return "", err
	}
	return dir, nil
}

// PublishEdit validates the page's live files and restarts its runtime. It does
// not copy, roll back, or automatically publish individual editing tool calls.
func (m *Manager) PublishEdit(ctx context.Context, instanceID, projectID, by string) (*PublishResult, error) {
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
	source := m.pageSourceDir(in.ID)
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

func (m *Manager) pageSourceDir(instanceID string) string {
	return filepath.Join(m.root, "page-sources", instanceID, "files")
}

// Import into the permanent directory, never rename or delete that directory.
// Explicit task publication replaces file contents in place; last writer wins.
func (m *Manager) replacePageSources(instanceID, source string) error {
	dir := m.pageSourceDir(instanceID)
	if filepath.Clean(source) == filepath.Clean(dir) {
		return nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, rel)
		if err := checkSourceTarget(dir, target); err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported source file: %s", rel)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, info.Mode().Perm()); err != nil {
			return err
		}
		return os.Chmod(target, info.Mode().Perm())
	})
	return err
}

func checkSourceTarget(root, target string) error {
	for path := target; ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			if path == root {
				return nil
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("source path contains a symbolic link")
		}
		if path == root {
			return nil
		}
	}
}
