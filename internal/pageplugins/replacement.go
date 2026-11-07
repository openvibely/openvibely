package pageplugins

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
)

func (m *Manager) stageReplacement(ctx context.Context, in *models.PagePluginInstance, pkg *models.PagePluginPackage) error {
	lifecycle := m.instanceLock("lifecycle:" + in.ID)
	lifecycle.Lock()
	defer lifecycle.Unlock()
	// Cancel active calls before waiting for admission, just as disable does.
	m.mu.Lock()
	if life := m.lifetimes[in.ID]; life != nil {
		life.cancel()
	}
	m.mu.Unlock()
	lock := m.instanceLock(in.ID)
	lock.Lock()
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	m.releaseInstance(in.ID)
	if m.OnDisable != nil {
		if err := m.OnDisable(ctx, in.ID); err != nil {
			if in.Enabled {
				return errors.Join(err, m.enableLocked(ctx, in.ID))
			}
			return err
		}
	}
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

func (m *Manager) enableReplacementLocked(ctx context.Context, id string) error {
	previous, err := m.store.GetReplacement(ctx, id)
	if err != nil {
		return err
	}
	err = m.enableLocked(ctx, id)
	if previous == nil {
		return err
	}
	// Missing administrator consent is a pending update, not a failed launch.
	if errors.Is(err, ErrApprovalRequired) {
		return err
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err != nil {
		failed, readErr := m.store.GetInstance(cleanupCtx, id)
		m.releaseInstance(id)
		if restoreErr := m.store.FinishReplacement(cleanupCtx, id, true); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("restoring previous package: %w", restoreErr))
		}
		if previous.Enabled {
			if restoreErr := m.enableLocked(cleanupCtx, id); restoreErr != nil {
				return errors.Join(err, fmt.Errorf("previous package restored but startup failed: %w", restoreErr))
			}
		}
		if readErr == nil && failed != nil {
			m.removeUnusedReplacementPackage(cleanupCtx, failed.PackageID)
		}
		return fmt.Errorf("page update failed; previous package restored: %w", err)
	}
	if err := m.store.FinishReplacement(cleanupCtx, id, false); err != nil {
		return fmt.Errorf("page started but update finalization needs retry: %w", err)
	}
	m.removeUnusedReplacementPackage(cleanupCtx, previous.PackageID)
	return nil
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
