package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/openvibely/openvibely/internal/persistence"
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
		return models.ComputerSettings{Resources: models.DefaultComputerResources(), IdleMinutes: 30, TimeZone: "UTC"}, nil
	}
	if err != nil {
		return models.ComputerSettings{}, err
	}
	return models.ComputerSettings{Resources: c.Resources, Enabled: c.Enabled, IdleMinutes: c.IdleMinutes, TimeZone: c.TimeZone}, nil
}

func (s *ProjectComputerService) SaveSettings(ctx context.Context, id string, v models.ComputerSettings) error {
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
	if p, ok := s.provisioner.(*LocalComputerProvisioner); ok {
		if old.Resources.CPUs != v.Resources.CPUs || old.Resources.MemoryGiB != v.Resources.MemoryGiB {
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
				if rows[0].State.Running {
					return errors.New("Stop the project computer before changing CPU or memory")
				}
			}
		}
	} else if old.Resources != v.Resources {
		return errors.New("Resource settings are managed by the hosted computer provider")
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
	s.publishStatus(id, true)
	return nil
}
