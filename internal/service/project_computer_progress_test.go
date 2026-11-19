package service

import (
	"context"
	"errors"
	"io"
	"net/http"
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
