package update

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/buildinfo"
)

func TestAutomaticUpdatePreferencePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coordinator.json")
	newCoordinator := func() *Coordinator {
		return NewCoordinator(nil, CurrentBuild{Distribution: "binary"}, "stable", nil, nil, false, "", nil)
	}
	c := newCoordinator()
	if err := c.SetPersistence(path); err != nil {
		t.Fatal(err)
	}
	if !c.Snapshot().AutomaticUpdates {
		t.Fatal("automatic updates must default to enabled")
	}
	if err := c.SetAutomaticUpdates(false); err != nil {
		t.Fatal(err)
	}
	restored := newCoordinator()
	if err := restored.SetPersistence(path); err != nil {
		t.Fatal(err)
	}
	if restored.Snapshot().AutomaticUpdates {
		t.Fatal("disabled preference was lost")
	}
	restored.stateWriter = func(string, []byte) error { return errors.New("disk full") }
	if err := restored.SetAutomaticUpdates(true); err == nil {
		t.Fatal("expected save error")
	}
	if restored.Snapshot().AutomaticUpdates {
		t.Fatal("failed save changed preference")
	}
}

func TestAutomaticUpdateWaitsForWorkAndApplies(t *testing.T) {
	now := time.Now().UTC()
	tracker := NewWorkTracker()
	done, err := tracker.Start(WorkTask)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	drain := NewDrainManager(tracker.Active, nil, 0, nil)
	drain.SetWorkTracker(tracker)
	installer := &countingInstaller{}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json")})
	c := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: "binary"}, "stable", drain, installer, false, "", nil)
	statePath := filepath.Join(t.TempDir(), "coordinator.json")
	if err := c.SetPersistence(statePath); err != nil {
		t.Fatal(err)
	}
	c.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	c.staged, c.state = *c.release, StateAvailable
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.StartRecovery(ctx)
	if err := c.accept(ctx, 10*time.Minute, true); err != nil {
		t.Fatal(err)
	}
	waitForCoordinatorState(t, c, StateWaitingForIdle)
	if installer.applies.Load() != 0 {
		t.Fatal("updated while work was running")
	}
	done()
	waitForCoordinatorState(t, c, StateSucceeded)
	if installer.applies.Load() != 1 {
		t.Fatal("update was not applied exactly once")
	}
	restored := NewCoordinator(client, c.current, "stable", NewDrainManager(nil, nil, 0, nil), installer, false, "", nil)
	if err := restored.SetPersistence(statePath); err != nil {
		t.Fatal(err)
	}
	if restored.autoAttemptedVersion != "0.6.0" {
		t.Fatal("automatic attempt was not persisted")
	}

}

func TestAutomaticUpdateEligibility(t *testing.T) {
	for _, name := range []string{"disabled", "manual", "source", "hosted", "configuration_error", "already_attempted", "cancelled_context"} {
		t.Run(name, func(t *testing.T) {
			c := NewCoordinator(nil, CurrentBuild{Distribution: "binary"}, "stable", nil, nil, false, "", nil)
			c.state = StateAvailable
			c.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0"}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch name {
			case "disabled":
				c.SetAutomaticUpdates(false)
			case "manual":
				c.manual = true
			case "source":
				c.current.Distribution = "source"
			case "hosted":
				c.SetManagedStateProvider(func() ManagedUpdateState { return ManagedUpdateState{} })
			case "configuration_error":
				c.configError = "unavailable"
			case "already_attempted":
				c.autoAttemptedVersion = "0.6.0"
			case "cancelled_context":
				cancel()
			}
			if err := c.accept(ctx, time.Minute, true); err != nil {
				t.Fatalf("ineligible update reached acceptance: %v", err)
			}
			if c.accepted {
				t.Fatal("ineligible update accepted")
			}
		})
	}
}

func TestBackgroundChecksAutomaticallyInstallCachedRelease(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			now := time.Now().UTC()
			client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json")})
			target := Target{ID: "linux-amd64", Kind: "executable", OS: "linux", Arch: "amd64", URL: "https://updates.example.test/app.tar.gz", Filetype: "tar.gz", Size: 3, SHA256: "0000000000000000000000000000000000000000000000000000000000000000"}
			release := cachedReleaseFixture(now, target, "download")
			if err := client.saveState(persistedClientState{LastSuccessfulCheck: now, Cached: release}); err != nil {
				t.Fatal(err)
			}
			installer := &countingInstaller{}
			c := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0", OS: "linux", Arch: "amd64"}, Distribution: "binary"}, "stable", NewDrainManager(nil, nil, 0, nil), installer, false, "", nil)
			if err := c.SetAutomaticUpdates(enabled); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.Start(ctx)
			defer c.StopChecks()
			if enabled {
				waitForCoordinatorState(t, c, StateSucceeded)
				if installer.applies.Load() != 1 {
					t.Fatal("background update not applied")
				}
			} else {
				deadline := time.Now().Add(time.Second)
				for installer.stages.Load() == 0 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				c.StopChecks()
				if c.Snapshot().State != StateAvailable || installer.applies.Load() != 0 {
					t.Fatal("disabled automatic update was applied")
				}
			}
		})
	}
}
