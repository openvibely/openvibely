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

// Publish installs a task-authored package and registers a page in its owning
// project. It never approves or launches native code. Identical retries reuse
// the existing package and project instance.
func (m *Manager) Publish(ctx context.Context, source, projectID, pageID, by string) (*PublishResult, error) {
	if projectID == "" || projectID == models.AllProjectsID {
		return nil, errors.New("a real owning project is required")
	}
	m.publishMu.Lock()
	defer m.publishMu.Unlock()
	// Validate the page choice before creating any installed package records.
	man, _, err := validatePackageFS(os.DirFS(source))
	if err != nil {
		return nil, err
	}
	if pageID == "" {
		pageID = man.Pages[0].ID
	}
	if _, ok := man.Page(pageID); !ok {
		return nil, errors.New("page_id is not declared in plugin.json")
	}
	pkg, err := m.InstallFromDir(ctx, source, by)
	if err != nil {
		return nil, err
	}
	man, err = m.Manifest(pkg)
	if err != nil {
		return nil, err
	}
	instances, err := m.store.ListInstances(ctx, projectID, false)
	if err != nil {
		return nil, err
	}
	var instance *models.PagePluginInstance
	for i := range instances {
		if instances[i].PackageID == pkg.ID && instances[i].PageID == pageID {
			instance = &instances[i]
			break
		}
	}
	if instance == nil {
		instance, err = m.CreateInstance(ctx, projectID, pkg.ID, pageID)
		if err != nil {
			return nil, err
		}
	}
	if !man.RunsProcess() {
		if err = m.Enable(ctx, instance.ID); err != nil {
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
	return &PublishResult{Instance: instance, ApprovalRequired: man.RunsProcess() && !instance.Enabled}, nil
}
