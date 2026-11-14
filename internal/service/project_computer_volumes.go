package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// Existing volumes retain their labels (including legacy volumes with no labels).
// Only newly created volumes are marked for automatic orphan cleanup.
func (p *LocalComputerProvisioner) ensureHomeVolume(ctx context.Context, id string) error {
	name := computerContainerName(id) + "-home"
	out, err := p.docker(ctx, nil, "volume", "ls", "--filter", "name=^"+name+"$", "--format", "{{.Name}}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) != "" {
		return nil
	}
	_, err = p.docker(ctx, nil, "volume", "create", "--label", "openvibely.managed=true", "--label", "openvibely.project="+id, name)
	return err
}

type computerVolume struct {
	Name   string
	Labels map[string]string
}

func (p *LocalComputerProvisioner) inspectManagedVolume(ctx context.Context, name string) (*computerVolume, error) {
	raw, err := p.docker(ctx, nil, "volume", "inspect", name)
	if err != nil {
		return nil, err
	}
	var rows []computerVolume
	if json.Unmarshal(raw, &rows) != nil || len(rows) != 1 {
		return nil, errors.New("invalid volume metadata")
	}
	v := rows[0]
	id := v.Labels["openvibely.project"]
	if v.Name != name || v.Labels["openvibely.managed"] != "true" || id == "" || name != computerContainerName(id)+"-home" {
		return nil, errors.New("volume ownership mismatch")
	}
	return &v, nil
}

// Docker refuses removal of a referenced volume too, protecting against a
// container being attached after the explicit all-containers check.
func (p *LocalComputerProvisioner) removeUnattachedVolume(ctx context.Context, name string) error {
	if _, err := p.inspectManagedVolume(ctx, name); err != nil {
		return err
	}
	refs, err := p.docker(ctx, nil, "container", "ls", "--all", "--filter", "volume="+name, "--format", "{{.ID}}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(refs)) != "" {
		return nil
	}
	_, err = p.docker(ctx, nil, "volume", "rm", name)
	return err
}

func (s *ProjectComputerService) startOrphanVolumeCleanup(parent context.Context) {
	p, ok := s.provisioner.(*LocalComputerProvisioner)
	if !ok {
		return
	}
	value, _ := s.locks.LoadOrStore("orphan-volume-cleanup", &sync.Mutex{})
	lock := value.(*sync.Mutex)
	if !lock.TryLock() {
		return
	}
	go func() {
		defer lock.Unlock()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 2*time.Minute)
		defer cancel()
		// Failures stay discoverable by their volume labels on the next start.
		s.cleanupOrphanVolumes(ctx, p)
	}()
}

func (s *ProjectComputerService) cleanupOrphanVolumes(ctx context.Context, p *LocalComputerProvisioner) {
	names, err := p.docker(ctx, nil, "volume", "ls", "--filter", "label=openvibely.managed=true", "--format", "{{.Name}}")
	if err != nil {
		return
	}
	for _, name := range strings.Fields(string(names)) {
		if ctx.Err() != nil {
			return
		}
		volume, err := p.inspectManagedVolume(ctx, name)
		if err != nil {
			continue
		}
		id := volume.Labels["openvibely.project"]
		gate := s.lifecycle(id)
		// A busy project's next start can retry; never wait behind provisioning.
		if !gate.TryLock() {
			continue
		}
		project, err := s.projects.GetByID(ctx, id)
		if err == nil && project == nil {
			_ = p.removeUnattachedVolume(ctx, name)
		}
		gate.Unlock()
	}
}
