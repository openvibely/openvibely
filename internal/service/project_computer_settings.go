package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

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
