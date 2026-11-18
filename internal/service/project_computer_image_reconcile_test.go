package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

func TestComputerImageReconciliationFollowsEnabledToggle(t *testing.T) {
	for _, name := range []string{"running", "stopped", "current-stopped", "disabled", "disabled-running", "disabled-stop-failure", "busy", "remove-failure", "start-failure"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("OPENVIBELY_COMPUTER_IMAGE", "new-image")
			db := teststores.New(t)
			project := &models.Project{Name: name}
			require.NoError(t, db.Projects.Create(t.Context(), project))
			var unhealthy atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if unhealthy.Load() {
					w.WriteHeader(503)
					return
				}
				_, _ = w.Write([]byte(`{"isolation":1,"viewer_connections":1}`))
			}))
			defer server.Close()
			port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
			running := name != "stopped" && name != "current-stopped" && name != "disabled"
			image := "old-image"
			if name == "current-stopped" {
				image = "new-image"
			}
			failStop := name == "disabled-stop-failure"
			failRemove, failStart := name == "remove-failure", name == "start-failure"
			mutations := []string{}
			cleanups := 0
			p := &LocalComputerProvisioner{run: func(_ context.Context, _ io.Reader, args ...string) ([]byte, error) {
				switch args[0] {
				case "image":
					cleanups++
					return nil, nil
				case "info":
					return nil, nil
				case "container":
					return []byte(computerContainerName(project.ID)), nil
				case "inspect":
					return json.Marshal([]any{map[string]any{
						"Config": map[string]any{"Image": image, "Labels": map[string]string{"openvibely.project": project.ID, "openvibely.computer-isolation": "1"}, "Env": []string{"TZ=UTC", "OPENVIBELY_COMPUTER_TOKEN=test"}},
						"State":  map[string]bool{"Running": running}, "HostConfig": map[string]int64{"NanoCpus": 2e9, "Memory": 4 << 30, "PidsLimit": 4096},
						"NetworkSettings": map[string]any{"Ports": map[string]any{"8090/tcp": []map[string]string{{"HostIP": "127.0.0.1", "HostPort": port}}}},
					}})
				case "stop":
					if failStop {
						failStop = false
						return nil, errors.New("stop failed")
					}
					running = false
				case "rm":
					if failRemove {
						failRemove = false
						return nil, errors.New("remove failed")
					}
				case "run":
					image = "new-image"
					if failStart {
						failStart = false
						return nil, errors.New("start failed")
					}
					running = true
				case "start":
					running = true
				case "update":
				default:
					return nil, errors.New("unexpected Docker operation: " + args[0])
				}
				mutations = append(mutations, args[0])
				return nil, nil
			}}
			svc := NewProjectComputerService(db.Projects, p)
			require.NoError(t, svc.repo.Configure(t.Context(), project.ID, "local"))
			require.NoError(t, svc.repo.SetEnabled(t.Context(), project.ID, !strings.HasPrefix(name, "disabled")))
			if name == "busy" {
				require.NoError(t, svc.repo.Reserve(t.Context(), project.ID, "agent"))
			}
			if failStop {
				require.ErrorContains(t, svc.reconcileComputerImage(t.Context(), project.ID), "stop failed")
				require.True(t, running)
			}
			if failRemove || failStart {
				require.Error(t, svc.reconcileComputerImage(t.Context(), project.ID))
				require.False(t, running)
			}
			err := svc.reconcileComputerImage(t.Context(), project.ID)
			if name == "busy" {
				require.Error(t, err)
				require.Empty(t, mutations)
				return
			}
			require.NoError(t, err)
			if strings.HasPrefix(name, "disabled") {
				require.False(t, running)
				if name == "disabled" {
					require.Empty(t, mutations)
				} else {
					require.Equal(t, []string{"stop"}, mutations)
				}
				mutations = nil
				require.NoError(t, svc.reconcileComputerImage(t.Context(), project.ID))
				require.Empty(t, mutations)
				return
			}
			require.True(t, running)
			if name == "running" || name == "stopped" || name == "remove-failure" {
				require.Equal(t, 1, cleanups)
			}
			cleanupCount := cleanups
			require.Equal(t, "new-image", image)
			mutations = nil
			require.NoError(t, svc.reconcileComputerImage(t.Context(), project.ID))
			require.Empty(t, mutations)
			require.Equal(t, cleanupCount, cleanups)
			// A current, running container must still pass readiness.
			unhealthy.Store(true)
			require.ErrorContains(t, svc.reconcileComputerImage(t.Context(), project.ID), "not ready")
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
