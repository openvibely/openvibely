package pageplugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

type PublishResult struct {
	Instance *models.PagePluginInstance
}

// PublishToInstance installs a task-authored package and registers or updates a
// page in its owning project. An empty instanceID selects the project instance
// automatically. Enabled replacements start their backend immediately. Identical retries
// reuse the existing package and project instance.
func (m *Manager) PublishToInstance(ctx context.Context, source, projectID, pageID, by, instanceID string) (*PublishResult, error) {
	if projectID == "" || projectID == models.AllProjectsID {
		return nil, errors.New("a real owning project is required")
	}
	m.publishMu.Lock()
	defer m.publishMu.Unlock()
	return m.publishLocked(ctx, source, projectID, pageID, by, instanceID)
}

func (m *Manager) publishLocked(ctx context.Context, source, projectID, pageID, by, instanceID string) (*PublishResult, error) {
	// Validate the page choice before creating any installed package records.
	man, _, err := validatePackageFS(os.DirFS(source))
	if err != nil {
		return nil, err
	}
	if instanceID != "" {
		target, err := m.store.GetInstance(ctx, instanceID)
		if err != nil {
			return nil, err
		}
		if target == nil || target.ProjectID != projectID {
			return nil, ErrNotFound
		}
		if pageID == "" {
			pageID = target.PageID
		}
	}
	if pageID == "" {
		pageID = man.Pages[0].ID
	}
	if _, ok := man.Page(pageID); !ok {
		return nil, errors.New("page_id is not declared in plugin.json")
	}
	instances, err := m.store.ListInstances(ctx, projectID, false)
	if err != nil {
		return nil, err
	}
	var instance *models.PagePluginInstance
	for i := range instances {
		in := &instances[i]
		if instanceID != "" && in.ID == instanceID {
			if in.PluginID != man.ID || in.PageID != pageID {
				return nil, errors.New("replacement must retain plugin ID and page ID")
			}
			instance = in
			break
		}
		if instanceID == "" && in.PluginID == man.ID && in.PageID == pageID {
			if instance != nil {
				return nil, errors.New("multiple matching pages; supply instance_id")
			}
			instance = in
		}
	}
	if instanceID != "" && instance == nil {
		return nil, ErrNotFound
	}
	// Publishing never overrides an administrator's decision to disable a page.
	enable := instance == nil
	if instance != nil {
		pending, err := m.store.GetReplacement(ctx, instance.ID)
		if err != nil {
			return nil, err
		}
		if pending != nil && instance.Version != man.Version {
			return nil, errors.New("finish the pending page update before publishing another version")
		}
		enable = instance.Enabled
		if pending != nil {
			enable = pending.Enabled
		}
	}
	// Task publications and side chat share one instance-owned source tree.
	// Import before starting the replacement so failed startup retains the
	// submitted files for repair. There is no previous runtime snapshot.
	if instance != nil {
		if err := m.replacePageSources(instance.ID, source); err != nil {
			return nil, err
		}
		source = m.pageSourceDir(instance.ID)
	}
	existing, err := m.store.GetPackageByVersion(ctx, man.ID, man.Version)
	if err != nil {
		return nil, err
	}
	var pkg *models.PagePluginPackage
	if instance == nil {
		pkg, err = m.InstallFromDir(ctx, source, by)
	} else {
		// Register publication metadata against the permanent files. Do not
		// create another installed copy of a page being edited.
		var raw []byte
		man, _, err = validatePackageFS(os.DirFS(source))
		if err == nil {
			raw, err = os.ReadFile(filepath.Join(source, ManifestFile))
		}
		if err == nil {
			var hash string
			hash, err = HashTree(os.DirFS(source))
			if err == nil && existing != nil && existing.ContentHash == hash {
				pkg = existing
			} else if err == nil {
				if existing != nil {
					man.Version = fmt.Sprintf("%s-edit.%d", man.Version, time.Now().UnixNano())
					raw, err = json.MarshalIndent(man, "", "  ")
					if err == nil {
						err = os.WriteFile(filepath.Join(source, ManifestFile), raw, 0644)
					}
					if err == nil {
						hash, err = HashTree(os.DirFS(source))
					}
				}
				if err == nil {
					pkg = &models.PagePluginPackage{PluginID: man.ID, Version: man.Version, Name: man.Name, ManifestJSON: string(raw), ContentHash: hash, InstallPath: source, InstalledBy: by}
					err = m.store.CreatePackage(ctx, pkg)
				}
			}
		}
	}
	if err != nil {
		return nil, err
	}
	discardNew := func() {
		if existing == nil {
			m.removeUnusedReplacementPackage(context.WithoutCancel(ctx), pkg.ID)
		}
	}
	man, err = m.Manifest(pkg)
	if err != nil {
		discardNew()
		return nil, err
	}
	if instance == nil {
		instance, err = m.CreateInstance(ctx, projectID, pkg.ID, pageID)
		if err != nil {
			discardNew()
			return nil, err
		}
	}
	if instance.PackageID != pkg.ID {
		if err = m.stageReplacement(ctx, instance, pkg); err != nil {
			discardNew()
			return nil, err
		}
		if err = m.store.FinishReplacement(ctx, instance.ID); err != nil {
			return nil, err
		}
		m.removeUnusedReplacementPackage(context.WithoutCancel(ctx), instance.PackageID)
	}
	m.releaseInstanceRuntime(instance.ID, false)
	{
		if enable {
			err = m.Enable(ctx, instance.ID)
		} else {
			err = m.finishDisabledReplacement(ctx, instance.ID)
		}
		if err != nil {
			// Keep chat reachable so the user can repair the same live files.
			if enable {
				_ = m.store.SetInstanceEnabled(context.WithoutCancel(ctx), instance.ID, true)
			}
			return nil, err
		}
	}
	instance, err = m.store.GetInstance(ctx, instance.ID)
	if err != nil {
		return nil, err
	}
	if instance == nil {
		return nil, ErrNotFound
	}
	if err := m.replacePageSources(instance.ID, source); err != nil {
		return nil, err
	}
	return &PublishResult{Instance: instance}, nil
}
