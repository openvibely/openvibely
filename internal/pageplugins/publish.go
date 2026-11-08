package pageplugins

import (
	"context"
	"errors"
	"os"

	"github.com/openvibely/openvibely/internal/models"
)

type PublishResult struct {
	Instance         *models.PagePluginInstance
	ApprovalRequired bool
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
	// Import before starting the replacement so failed builds/startups retain
	// the submitted files while the previous runtime snapshot remains available.
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
	pkg, err := m.InstallFromDir(ctx, source, by)
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
	}
	{
		if enable {
			err = m.Enable(ctx, instance.ID)
		} else {
			err = m.finishDisabledReplacement(ctx, instance.ID)
		}
		if err != nil {
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
	return &PublishResult{Instance: instance, ApprovalRequired: false}, nil
}
