package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/openvibely/openvibely/internal/persistence"
	"strconv"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

func (s *ProjectComputerService) ResourceSettings(ctx context.Context, id string) (models.ComputerResources, error) {
	if _, ok := s.provisioner.(*LocalComputerProvisioner); !ok {
		return models.ComputerResources{}, errors.New("Resource settings are managed by the hosted computer provider")
	}
	c, err := s.projects.Computers().Get(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return models.DefaultComputerResources(), nil
	}
	if err != nil {
		return models.ComputerResources{}, err
	}
	return c.Resources, nil
}

func (s *ProjectComputerService) SetResourceSettings(ctx context.Context, id string, v models.ComputerResources) error {
	if v.CPUs < 1 || v.CPUs > 64 || v.MemoryGiB < 1 || v.MemoryGiB > 256 || v.PidsLimit < 256 || v.PidsLimit > 65536 {
		return errors.New("Use 1–64 CPUs, 1–256 GiB memory, and 256–65,536 processes/threads")
	}
	gate := s.lifecycle(id)
	gate.Lock()
	defer gate.Unlock()
	old, err := s.ResourceSettings(ctx, id)
	if err != nil {
		return err
	}
	p := s.provisioner.(*LocalComputerProvisioner)
	// Listing distinguishes an absent container from a Docker connection failure.
	raw, err := p.docker(ctx, nil, "container", "ls", "--all", "--filter", "name=^/"+computerContainerName(id)+"$", "--format", "{{.Names}}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(raw)) != "" {
		raw, err = p.docker(ctx, nil, "inspect", computerContainerName(id))
		if err != nil {
			return err
		}
		var rows []struct {
			Config struct{ Labels map[string]string }
			State  struct{ Running bool }
		}
		if json.Unmarshal(raw, &rows) != nil || len(rows) != 1 || rows[0].Config.Labels["openvibely.project"] != id {
			return errors.New("computer ownership mismatch")
		}
		if rows[0].State.Running && (old.CPUs != v.CPUs || old.MemoryGiB != v.MemoryGiB) {
			return errors.New("Stop the project computer before changing CPU or memory")
		}
	}
	if err = s.projects.Computers().Configure(ctx, id, s.provisioner.Provider()); err != nil {
		return err
	}
	if err = s.projects.Computers().SetResources(ctx, id, v); err != nil {
		return err
	}
	// Readiness reconciles the saved process limit. Persisting first ensures future
	// retries also use the desired value if Docker is temporarily unavailable.
	s.publishStatus(id, true)
	return nil
}

func (s *ProjectComputerService) Settings(ctx context.Context, id string) (models.ComputerSettings, error) {
	c, err := s.projects.Computers().Get(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return models.ComputerSettings{PreviewQuality: "balanced", Resources: models.DefaultComputerResources(), IdleMinutes: 30, TimeZone: "UTC"}, nil
	}
	if err != nil {
		return models.ComputerSettings{}, err
	}
	return models.ComputerSettings{PreviewQuality: c.PreviewQuality, Resources: c.Resources, Enabled: c.Enabled, IdleMinutes: c.IdleMinutes, TimeZone: c.TimeZone}, nil
}

func (s *ProjectComputerService) SaveSettings(ctx context.Context, id string, v models.ComputerSettings) error {
	if v.PreviewQuality == "" {
		v.PreviewQuality = "balanced"
	}
	switch v.PreviewQuality {
	case "low", "balanced", "high":
	default:
		return errors.New("Choose Low, Balanced, or High preview quality")
	}
	if v.Resources.CPUs < 1 || v.Resources.CPUs > 64 || v.Resources.MemoryGiB < 1 || v.Resources.MemoryGiB > 256 || v.Resources.PidsLimit < 256 || v.Resources.PidsLimit > 65536 {
		return errors.New("Use 1–64 CPUs, 1–256 GiB memory, and 256–65,536 processes/threads")
	}
	if err := validateComputerTimeZone(v.TimeZone); err != nil {
		return err
	}
	if v.IdleMinutes < 0 || v.IdleMinutes > 10080 {
		return errors.New("Choose an idle timeout between 1 and 10080 minutes, or Never")
	}
	gate := s.lifecycle(id)
	gate.Lock()
	defer gate.Unlock()
	old, err := s.Settings(ctx, id)
	if err != nil {
		return err
	}
	repo := s.projects.Computers()
	restart := false
	updateLimit := false
	var local *LocalComputerProvisioner
	if p, ok := s.provisioner.(*LocalComputerProvisioner); ok {
		local = p
		if v.Enabled {
			actual, err := p.inspectSettings(ctx, id)
			if err != nil {
				return err
			}
			if actual != nil {
				restart = !actual.matchesDesktop(v) || !actual.State.Running
				updateLimit = !restart && actual.HostConfig.PidsLimit != int64(v.Resources.PidsLimit)
			} else if old.Enabled {
				// A failed replacement may have removed the old container before creation failed.
				restart = true
			}
		}
	} else if old.Resources != v.Resources {
		return errors.New("Resource settings are managed by the hosted computer provider")
	}
	if restart {
		busy, err := repo.SessionsBusy(ctx, id)
		if err != nil {
			return err
		}
		if busy {
			return persistence.ErrComputerBusy
		}
		state, err := repo.Get(ctx, id)
		if err != nil {
			return err
		}
		if state.Owner != "" && state.LeaseUntil >= time.Now().Unix() {
			return persistence.ErrComputerBusy
		}
	}
	persist := func() error { return repo.SaveSettings(ctx, id, s.provisioner.Provider(), v) }
	if old.Enabled && !v.Enabled {
		busy, err := repo.SessionsBusy(ctx, id)
		if err != nil {
			return err
		}
		if busy {
			return persistence.ErrComputerBusy
		}
		state, err := repo.Get(ctx, id)
		if err != nil {
			return err
		}
		if state.Owner != "" && state.LeaseUntil >= time.Now().Unix() {
			return persistence.ErrComputerBusy
		}
		return s.changeViewerPermission(id, true, func() error {
			if _, err := s.provisioner.Resolve(ctx, id); err == nil {
				if err = s.provisioner.Stop(ctx, id); err != nil {
					return err
				}
			}
			return persist()
		})
	}
	if err := persist(); err != nil {
		return err
	}
	defer s.publishStatus(id, true)
	if restart {
		if err := s.startLocked(ctx, id, false); err != nil {
			return fmt.Errorf("Settings saved, but the computer could not restart: %w", err)
		}
	}
	if updateLimit {
		p := s.provisioner.(*LocalComputerProvisioner)
		if _, err := p.docker(ctx, nil, "update", "--pids-limit", strconv.Itoa(v.Resources.PidsLimit), computerContainerName(id)); err != nil {
			return fmt.Errorf("Settings saved, but the process limit could not be applied: %w", err)
		}
	}
	if local != nil && v.Enabled {
		actual, err := local.inspectSettings(ctx, id)
		if err != nil {
			return fmt.Errorf("Settings saved, but verification failed: %w", err)
		}
		if actual != nil {
			if restart && !actual.State.Running {
				return errors.New("Settings saved, but the computer did not reach the expected running state. Save again to retry")
			}
			if !actual.matchesDesktop(v) || actual.HostConfig.PidsLimit != int64(v.Resources.PidsLimit) {
				return errors.New("Settings saved, but the computer does not match them. Save again to retry")
			}
			if actual.State.Running && !s.Ready(ctx, id) {
				return errors.New("Settings saved, but the computer is not ready. Save again to retry")
			}
		} else if restart {
			return errors.New("Settings saved, but the computer did not start. Save again to retry")
		}
	}
	return nil
}
