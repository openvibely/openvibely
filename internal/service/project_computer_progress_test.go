package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComputerBuildProgress(t *testing.T) {
	t.Setenv("OPENVIBELY_COMPUTER_IMAGE", "")
	for _, fail := range []bool{false, true} {
		var stages []string
		p := &LocalComputerProvisioner{run: func(_ context.Context, _ io.Reader, args ...string) ([]byte, error) {
			if args[0] == "image" {
				return nil, errors.New("missing")
			}
			require.Equal(t, "building", stages[len(stages)-1])
			if fail {
				return nil, errors.New("build failed")
			}
			return nil, nil
		}}
		err := p.ensureImage(t.Context(), func(stage string) { stages = append(stages, stage) })
		require.Equal(t, fail, err != nil)
		require.Equal(t, []string{"building", "starting"}, stages)
	}
	// Waiting for the same image reports building, and cancellation releases the waiter.
	computerImageBuildLock <- struct{}{}
	defer func() { <-computerImageBuildLock }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var stage string
	err := (&LocalComputerProvisioner{}).ensureImage(ctx, func(value string) { stage = value; cancel() })
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, "building", stage)
}

func TestComputerStartupProgressSharedAndCleared(t *testing.T) {
	_, svc, project, task := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) })
	scoped, err := svc.ForTask(t.Context(), project.ID, task.ID)
	require.NoError(t, err)
	svc.setStartupStage(project.ID, "building")
	require.Equal(t, "building", scoped.StartupStage(project.ID))
	require.Empty(t, scoped.StartupStage("other"))
	svc.setStartupStage(project.ID, "")
	require.Empty(t, scoped.StartupStage(project.ID))
}

func TestComputerTaskDesktopStartupProgress(t *testing.T) {
	for _, mode := range []string{"ready", "starting", "failed"} {
		t.Run(mode, func(t *testing.T) {
			var started atomic.Bool
			var healthCalls atomic.Int32
			_, svc, project, _ := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("probe") == "1" {
					if mode != "ready" {
						http.Error(w, "not started", 503)
						return
					}
				} else {
					healthCalls.Add(1)
					if !started.Load() {
						t.Error("blocking health called before starting status")
					}
					if mode == "failed" {
						http.Error(w, "startup failed", 503)
						return
					}
				}
				io.WriteString(w, `{"isolation":1,"viewer_connections":1}`)
			})
			ready := svc.ReadyWithProgress(t.Context(), project.ID, func() { started.Store(true) })
			require.Equal(t, mode != "failed", ready)
			require.Equal(t, mode != "ready", started.Load())
			if mode == "ready" {
				require.Zero(t, healthCalls.Load())
			} else {
				require.EqualValues(t, 1, healthCalls.Load())
			}
		})
	}
}
