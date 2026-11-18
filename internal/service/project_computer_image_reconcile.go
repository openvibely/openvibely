package service

import (
	"context"
	"os"
	"time"

	"github.com/openvibely/openvibely/internal/persistence"
)

// Maintenance upgrades enabled computers without starting stopped desktops.
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
	if err != nil {
		return err
	}
	if !state.Enabled || state.Provider != p.Provider() {
		return nil
	}
	actual, err := p.inspectSettings(ctx, id)
	if err != nil {
		return err
	}
	image := os.Getenv("OPENVIBELY_COMPUTER_IMAGE")
	if image == "" {
		image = computerImageTag()
	}
	if actual != nil && actual.Config.Image == image && actual.Config.Labels["openvibely.computer-isolation"] == "1" {
		return nil
	}
	busy, err := s.projects.Computers().SessionsBusy(ctx, id)
	if err != nil {
		return err
	}
	if busy || (state.Owner != "" && state.LeaseUntil >= time.Now().Unix()) {
		return persistence.ErrComputerBusy
	}
	if err := s.projects.Computers().MarkCleanupRequired(ctx, id); err != nil {
		return err
	}
	defer s.publishStatus(id, true)
	_, err = p.prepare(ctx, id, state.TimeZone, actual != nil && actual.State.Running)
	return err
}
