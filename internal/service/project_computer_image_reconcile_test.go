package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestComputerImageBuildShared(t *testing.T) {
	t.Setenv("OPENVIBELY_COMPUTER_IMAGE", "")
	var mu sync.Mutex
	built, builds := false, 0
	run := func(_ context.Context, _ io.Reader, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if args[0] == "image" {
			if !built {
				return nil, errors.New("missing")
			}
			return nil, nil
		}
		if args[0] == "build" {
			builds++
			built = true
			return nil, nil
		}
		return nil, errors.New("unexpected operation")
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- (&LocalComputerProvisioner{run: run}).ensureImage(t.Context()) }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	require.Equal(t, 1, builds)
}

func TestComputerImageReconciliationPreservesState(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		running, enabled, busy bool
	}{
		{"running", true, true, false}, {"stopped", false, true, false}, {"disabled", true, false, false}, {"busy", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPENVIBELY_COMPUTER_IMAGE", "new-image")
			db := teststores.New(t)
			project := &models.Project{Name: tc.name}
			require.NoError(t, db.Projects.Create(t.Context(), project))
			image, running := "old-image", tc.running
			mutations := []string{}
			p := &LocalComputerProvisioner{run: func(_ context.Context, _ io.Reader, args ...string) ([]byte, error) {
				switch args[0] {
				case "container":
					return []byte(computerContainerName(project.ID)), nil
				case "inspect":
					return json.Marshal([]any{map[string]any{
						"Config": map[string]any{"Image": image, "Labels": map[string]string{"openvibely.project": project.ID, "openvibely.computer-isolation": "1"}, "Env": []string{"TZ=UTC", "OPENVIBELY_COMPUTER_TOKEN=test"}},
						"State":  map[string]bool{"Running": running}, "HostConfig": map[string]int64{"NanoCpus": 2e9, "Memory": 4 << 30, "PidsLimit": 4096},
						"NetworkSettings": map[string]any{"Ports": map[string]any{"8090/tcp": []map[string]string{{"HostIP": "127.0.0.1", "HostPort": "1234"}}}},
					}})
				case "stop":
					running = false
				case "rm":
				case "run":
					running = true
					image = "new-image"
				case "create":
					running = false
					image = "new-image"
				default:
					return nil, errors.New("unexpected Docker operation")
				}
				mutations = append(mutations, args[0])
				return nil, nil
			}}
			svc := NewProjectComputerService(db.Projects, p)
			require.NoError(t, svc.repo.Configure(t.Context(), project.ID, "local"))
			require.NoError(t, svc.repo.SetEnabled(t.Context(), project.ID, tc.enabled))
			if tc.busy {
				require.NoError(t, svc.repo.Reserve(t.Context(), project.ID, "agent"))
			}
			err := svc.reconcileComputerImage(t.Context(), project.ID)
			if tc.busy {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.running, running)
			if !tc.enabled || tc.busy {
				require.Empty(t, mutations)
				return
			}
			require.Equal(t, "new-image", image)
			if tc.running {
				require.Equal(t, []string{"stop", "rm", "run"}, mutations)
			} else {
				require.Equal(t, []string{"rm", "create"}, mutations)
			}
			mutations = nil
			require.NoError(t, svc.reconcileComputerImage(t.Context(), project.ID))
			require.Empty(t, mutations)
		})
	}
}

func TestComputerImageBuildWaitCancellation(t *testing.T) {
	computerImageBuildLock <- struct{}{}
	defer func() { <-computerImageBuildLock }()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p := &LocalComputerProvisioner{run: func(context.Context, io.Reader, ...string) ([]byte, error) {
		t.Error("cancelled waiter invoked Docker")
		return nil, nil
	}}
	require.ErrorIs(t, p.ensureImage(ctx), context.Canceled)
}
