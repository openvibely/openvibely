package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestComputerResourceSettings(t *testing.T) {
	db := teststores.New(t)
	project := &models.Project{Name: "Resource settings"}
	require.NoError(t, db.Projects.Create(t.Context(), project))
	running := false
	failure := false
	p := &LocalComputerProvisioner{run: func(_ context.Context, _ io.Reader, args ...string) ([]byte, error) {
		if failure {
			return nil, errors.New("Docker unavailable")
		}
		switch args[0] {
		case "container":
			return []byte(computerContainerName(project.ID)), nil
		case "inspect":
			return json.Marshal([]any{map[string]any{"Config": map[string]any{"Labels": map[string]string{"openvibely.project": project.ID}}, "State": map[string]bool{"Running": running}}})
		default:
			t.Fatalf("unexpected Docker mutation: %v", args)
			return nil, nil
		}
	}}
	svc := NewProjectComputerService(db.Projects, p)
	defaults, err := svc.ResourceSettings(t.Context(), project.ID)
	require.NoError(t, err)
	require.Equal(t, models.DefaultComputerResources(), defaults)
	for _, bad := range []models.ComputerResources{{CPUs: 0, MemoryGiB: 4, PidsLimit: 4096}, {CPUs: 2, MemoryGiB: 0, PidsLimit: 4096}, {CPUs: 2, MemoryGiB: 4, PidsLimit: -1}, {CPUs: 65, MemoryGiB: 4, PidsLimit: 4096}} {
		require.Error(t, svc.SetResourceSettings(t.Context(), project.ID, bad))
	}
	custom := models.ComputerResources{CPUs: 3, MemoryGiB: 6, PidsLimit: 8192}
	require.NoError(t, svc.SetResourceSettings(t.Context(), project.ID, custom))
	saved, err := svc.ResourceSettings(t.Context(), project.ID)
	require.NoError(t, err)
	require.Equal(t, custom, saved)
	// A fresh service reads the durable per-project settings.
	saved, err = NewProjectComputerService(db.Projects, p).ResourceSettings(t.Context(), project.ID)
	require.NoError(t, err)
	require.Equal(t, custom, saved)
	running = true
	require.ErrorContains(t, svc.SetResourceSettings(t.Context(), project.ID, defaults), "Stop the project computer")
	custom.PidsLimit = 4096
	require.NoError(t, svc.SetResourceSettings(t.Context(), project.ID, custom))
	failure = true
	require.ErrorContains(t, svc.SetResourceSettings(t.Context(), project.ID, defaults), "Docker unavailable")
	saved, err = svc.ResourceSettings(t.Context(), project.ID)
	require.NoError(t, err)
	require.Equal(t, custom, saved)
}

func TestComputerProvisionerUsesResourceSettings(t *testing.T) {
	t.Setenv("OPENVIBELY_COMPUTER_IMAGE", "resource-fixture")
	resources := models.ComputerResources{CPUs: 3, MemoryGiB: 6, PidsLimit: 8192}
	created := false
	updates := 0
	p := &LocalComputerProvisioner{resources: func(context.Context, string) (models.ComputerResources, error) { return resources, nil }}
	p.run = func(_ context.Context, _ io.Reader, args ...string) ([]byte, error) {
		switch args[0] {
		case "inspect":
			if !created {
				return nil, errors.New("not found")
			}
			return json.Marshal([]any{map[string]any{"Config": map[string]any{"Image": "resource-fixture", "Labels": map[string]string{"openvibely.project": "project", "openvibely.computer-isolation": "1"}, "Env": []string{"OPENVIBELY_COMPUTER_TOKEN=fixture", "TZ=UTC", "OPENVIBELY_COMPUTER_CPUS=3", "OPENVIBELY_COMPUTER_MEMORY_GIB=6"}}, "State": map[string]bool{"Running": true}, "HostConfig": map[string]int{"PidsLimit": 4096}, "NetworkSettings": map[string]any{"Ports": map[string]any{"8090/tcp": []map[string]string{{"HostIP": "127.0.0.1", "HostPort": "1234"}}}}}})
		case "run":
			created = true
			command := strings.Join(args, " ")
			for _, flag := range []string{"--memory 6g", "--cpus 3", "--pids-limit 8192", "OPENVIBELY_COMPUTER_CPUS=3", "OPENVIBELY_COMPUTER_MEMORY_GIB=6"} {
				require.Contains(t, command, flag)
			}
		case "update":
			require.Equal(t, []string{"update", "--pids-limit", "8192", computerContainerName("project")}, args)
			updates++
		default:
			t.Fatalf("unexpected command: %v", args)
		}
		return nil, nil
	}
	_, err := p.Start(t.Context(), "project")
	require.NoError(t, err)
	_, err = p.ResolveCurrent(t.Context(), "project")
	require.NoError(t, err)
	require.Equal(t, 1, updates)
}
