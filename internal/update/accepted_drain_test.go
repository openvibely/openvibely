package update

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/buildinfo"
)

func TestAcceptedUpdateRenewsDrainAndRecoversExpiry(t *testing.T) {
	for _, expire := range []bool{false, true} {
		name := "long_running_task"
		if expire {
			name = "expired_lease"
		}
		t.Run(name, func(t *testing.T) {
			var unix atomic.Int64
			unix.Store(1000)
			now := func() time.Time { return time.Unix(unix.Load(), 0).UTC() }
			tracker := NewWorkTracker()
			done, err := tracker.Start(WorkTask)
			if err != nil {
				t.Fatal("start task")
			}
			defer done()
			drain := NewDrainManager(tracker.Active, nil, 0, now)
			drain.SetWorkTracker(tracker)
			root := t.TempDir()
			if err := drain.SetPersistence(filepath.Join(root, "drain.json")); err != nil {
				t.Fatal(err)
			}
			client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: now})
			installer := &countingInstaller{}
			c := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", drain, installer, false, "", nil)
			if err := c.SetPersistence(filepath.Join(root, "coordinator.json")); err != nil {
				t.Fatal(err)
			}
			c.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now().Add(24 * time.Hour)}}
			configureAcceptedOffer(t, c)
			c.staged = *c.release
			c.state = StateAvailable
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.StartRecovery(ctx)
			if err := c.Accept(ctx, 10*time.Minute); err != nil {
				t.Fatal(err)
			}
			waitForCoordinatorState(t, c, StateWaitingForIdle)
			first := drain.Status()
			if expire {
				unix.Add(601)
				if drain.Status().State != DrainStateIdle {
					t.Fatal("lease did not expire")
				}
				waitAcceptedDrain(t, c, func(s DrainStatus) bool { return s.Generation != "" && s.Generation != first.Generation })
			} else {
				// Simulate eighty minutes of work, advancing only after each renewal.
				for i := 0; i < 16; i++ {
					previous := drain.Status()
					unix.Add(300)
					waitAcceptedDrain(t, c, func(s DrainStatus) bool { return s.ExpiresAt.After(previous.ExpiresAt) })
				}
				if drain.Status().Generation != first.Generation {
					t.Fatal("renewal replaced active generation")
				}
			}
			if finish, err := tracker.Start(WorkTask); err == nil {
				finish()
				t.Fatal("new work admitted during drain")
			}
			done()
			waitForCoordinatorState(t, c, StateSucceeded)
		})
	}
}

func waitAcceptedDrain(t *testing.T, c *Coordinator, predicate func(DrainStatus) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s := c.Snapshot()
		if s.State == StateWaitingForIdle && predicate(s.Drain) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("accepted update did not renew/recover: %#v", c.Snapshot())
}

func TestAcceptedUpdateRestartAfterLeaseExpiry(t *testing.T) {
	for _, cancelBeforeRecovery := range []bool{false, true} {
		name := "resume"
		if cancelBeforeRecovery {
			name = "cancel"
		}
		t.Run(name, func(t *testing.T) {
			var unix atomic.Int64
			unix.Store(1000)
			now := func() time.Time { return time.Unix(unix.Load(), 0).UTC() }
			root := t.TempDir()
			drainPath, coordinatorPath := filepath.Join(root, "drain.json"), filepath.Join(root, "coordinator.json")
			client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: now})
			build := CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}
			oldDrain := NewDrainManager(nil, nil, 0, now)
			if err := oldDrain.SetPersistence(drainPath); err != nil {
				t.Fatal(err)
			}
			status, err := oldDrain.BeginDrain(DrainRequest{Lease: 10 * time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			old := NewCoordinator(client, build, "stable", oldDrain, &countingInstaller{}, false, "", nil)
			if err := old.SetPersistence(coordinatorPath); err != nil {
				t.Fatal(err)
			}
			old.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now().Add(24 * time.Hour)}}
			configureAcceptedOffer(t, old)
			build = old.current
			old.staged = *old.release
			old.state, old.operationGeneration = StateWaitingForIdle, status.Generation
			old.accepted, old.acceptanceLease = true, 10*time.Minute
			if err := old.persistLocked(); err != nil {
				t.Fatal(err)
			}
			// No live waiter: model a process crash and reopening after the deadline.
			unix.Add(601)
			drain := NewDrainManager(nil, nil, 0, now)
			if err := drain.SetPersistence(drainPath); err != nil {
				t.Fatal(err)
			}
			restored := NewCoordinator(client, build, "stable", drain, &countingInstaller{}, false, "", nil)
			if err := restored.SetPersistence(coordinatorPath); err != nil {
				t.Fatal(err)
			}
			if !restored.accepted || restored.state != StateAvailable {
				t.Fatal("restart lost approval")
			}
			if cancelBeforeRecovery {
				if err := restored.Cancel(); err != nil {
					t.Fatal(err)
				}
				again := NewCoordinator(client, build, "stable", drain, &countingInstaller{}, false, "", nil)
				if err := again.SetPersistence(coordinatorPath); err != nil {
					t.Fatal(err)
				}
				if again.accepted || again.state != StateIdle {
					t.Fatal("cancel was not durable")
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			restored.StartRecovery(ctx)
			if cancelBeforeRecovery {
				if restored.Snapshot().State != StateIdle {
					t.Fatal("cancelled update restarted")
				}
			} else {
				waitForCoordinatorState(t, restored, StateSucceeded)
			}
		})
	}
}
