package service

import (
	"context"
	"database/sql"
	"errors"
	"os"
)

// Maintenance keeps enabled computers current and running, and disabled ones stopped.
// The lifecycle lock also serializes normal starts and settings saves.
func (s *ProjectComputerService) reconcileComputerImage(ctx context.Context, id string) error {
	p, ok := s.provisioner.(*LocalComputerProvisioner)
	if !ok {
		return nil
	}
	gate := s.lifecycle(id)
	gate.Lock()
	defer gate.Unlock()
	state, err := s.projects.Computers().Get(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // Computer use was never configured for this project.
	}
	if err != nil {
		return err
	}
	if state.Provider != p.Provider() {
		return nil
	}
	actual, err := p.inspectSettings(ctx, id)
	if err != nil {
		return err
	}
	if !state.Enabled {
		if actual == nil || !actual.State.Running {
			return nil
		}
		if err := s.stopLocked(ctx, id); err != nil {
			return err
		}
		actual, err = p.inspectSettings(ctx, id)
		if err != nil {
			return err
		}
		if actual != nil && actual.State.Running {
			return errors.New("disabled project computer did not stop")
		}
		return nil
	}
	image := os.Getenv("OPENVIBELY_COMPUTER_IMAGE")
	if image == "" {
		image = computerImageTag()
	}
	if actual != nil && actual.State.Running && actual.Config.Image == image && actual.Config.Labels["openvibely.computer-isolation"] == "1" {
		if !s.Ready(ctx, id) {
			return errors.New("project computer is not ready")
		}
		return nil
	}
	// Normal startup checks active leases and waits for health/isolation readiness.
	return s.startLocked(ctx, id, false)
}
