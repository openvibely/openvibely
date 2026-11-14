package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestComputerVolumeCreationLabelsOnlyNewVolumes(t *testing.T) {
	for _, existing := range []bool{false, true} {
		var commands []string
		p := &LocalComputerProvisioner{run: func(_ context.Context, _ io.Reader, args ...string) ([]byte, error) {
			commands = append(commands, strings.Join(args, " "))
			if args[1] == "ls" && existing {
				return []byte(computerContainerName("p") + "-home"), nil
			}
			return nil, nil
		}}
		require.NoError(t, p.ensureHomeVolume(t.Context(), "p"))
		if existing {
			require.Len(t, commands, 1)
		} else {
			require.Len(t, commands, 2)
			require.Contains(t, commands[1], "--label openvibely.managed=true --label openvibely.project=p")
		}
	}
}

func TestComputerOrphanVolumeCleanupSafety(t *testing.T) {
	for _, scenario := range []string{"orphan", "existing project", "unlabeled", "wrong name", "stopped container", "locked", "removal failure"} {
		t.Run(scenario, func(t *testing.T) {
			db := teststores.New(t)
			id := "deleted-project"
			if scenario == "existing project" {
				project := &models.Project{Name: "Retained"}
				require.NoError(t, db.Projects.Create(t.Context(), project))
				id = project.ID
			}
			name := computerContainerName(id) + "-home"
			volume := computerVolume{Name: name, Labels: map[string]string{"openvibely.managed": "true", "openvibely.project": id}}
			if scenario == "unlabeled" {
				volume.Labels = nil
			}
			if scenario == "wrong name" {
				volume.Labels["openvibely.project"] = "another-project"
			}
			removes := 0
			p := &LocalComputerProvisioner{run: func(_ context.Context, _ io.Reader, args ...string) ([]byte, error) {
				if args[0] == "container" {
					require.Contains(t, args, "--all")
					if scenario == "stopped container" {
						return []byte("stopped-id"), nil
					}
					return nil, nil
				}
				switch args[1] {
				case "ls":
					return []byte(name), nil
				case "inspect":
					return json.Marshal([]computerVolume{volume})
				case "rm":
					removes++
					if scenario == "removal failure" {
						return nil, errors.New("busy")
					}
					return nil, nil
				}
				return nil, errors.New("unexpected command")
			}}
			svc := NewProjectComputerService(db.Projects, p)
			if scenario == "locked" {
				gate := svc.lifecycle(id)
				gate.Lock()
				defer gate.Unlock()
			}
			svc.cleanupOrphanVolumes(t.Context(), p)
			if scenario == "orphan" || scenario == "removal failure" {
				require.Equal(t, 1, removes)
			} else {
				require.Zero(t, removes)
			}
			if scenario == "removal failure" {
				svc.cleanupOrphanVolumes(t.Context(), p)
				require.Equal(t, 2, removes, "failed removal must remain retryable")
			}
		})
	}
}

func TestComputerOrphanCleanupBackgroundSingleScan(t *testing.T) {
	db := teststores.New(t)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	defer close(release)
	p := &LocalComputerProvisioner{run: func(ctx context.Context, _ io.Reader, args ...string) ([]byte, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	svc := NewProjectComputerService(db.Projects, p)
	ctx, cancel := context.WithCancel(t.Context())
	svc.startOrphanVolumeCleanup(ctx)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("background scan did not start")
	}
	cancel()
	// A second trigger must not wait for or start another scan.
	done := make(chan struct{})
	go func() { svc.startOrphanVolumeCleanup(t.Context()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("trigger blocked startup")
	}
	value, _ := svc.locks.Load("orphan-volume-cleanup")
	require.False(t, value.(*sync.Mutex).TryLock(), "scan must survive caller cancellation and stay exclusive")
	select {
	case <-entered:
		t.Fatal("overlapping scan")
	default:
	}
}
