package pageplugins

import (
	"context"
	"errors"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
)

func (m *Manager) stageReplacement(ctx context.Context, in *models.PagePluginInstance, pkg *models.PagePluginPackage) error {
	lifecycle := m.instanceLock("lifecycle:" + in.ID)
	lifecycle.Lock()
	defer lifecycle.Unlock()
	lock := m.instanceLock(in.ID)
	lock.Lock()
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	m.releaseInstanceRuntime(in.ID, false)
	// Replacement also admits a new reference to the installed package.
	m.packageMu.Lock()
	err := m.store.StageReplacement(ctx, in.ID, in.PackageID, pkg.ID)
	m.packageMu.Unlock()
	if err != nil {
		if in.Enabled {
			_ = m.enableLocked(ctx, in.ID)
		}
		return err
	}
	m.mu.Lock()
	delete(m.startups, in.ID)
	m.mu.Unlock()
	return nil
}

// Complete an interrupted metadata update, then start the current files. No
// previous code copy exists to roll back to.
func (m *Manager) enableReplacementLocked(ctx context.Context, id string) error {
	previous, err := m.store.GetReplacement(ctx, id)
	if err != nil {
		return err
	}
	if previous != nil {
		if err := m.store.FinishReplacement(ctx, id, false); err != nil {
			return err
		}
		m.removeUnusedReplacementPackage(context.WithoutCancel(ctx), previous.PackageID)
	}
	return m.enableLocked(ctx, id)
}

// finishDisabledReplacement commits an update to a disabled page without starting it.
func (m *Manager) finishDisabledReplacement(ctx context.Context, id string) error {
	lifecycle := m.instanceLock("lifecycle:" + id)
	lifecycle.Lock()
	defer lifecycle.Unlock()
	previous, err := m.store.GetReplacement(ctx, id)
	if err != nil || previous == nil {
		return err
	}
	if err := m.store.FinishReplacement(ctx, id, false); err != nil {
		return err
	}
	m.removeUnusedReplacementPackage(ctx, previous.PackageID)
	return nil
}

func (m *Manager) removeUnusedReplacementPackage(ctx context.Context, id string) {
	if err := m.RemovePackage(ctx, id); err != nil && !errors.Is(err, ErrPackageInUse) && !errors.Is(err, ErrNotFound) {
		applog.Infof("[pageplugins] unused update package cleanup failed: %v", err)
	}
}
