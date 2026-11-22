package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/buildinfo"
)

type retryLocalInstaller struct {
	countingInstaller
	path string
}

func (i *retryLocalInstaller) Stage(ctx context.Context, release VerifiedRelease) (any, error) {
	i.stages.Add(1)
	if i.stageErr != nil {
		return nil, i.stageErr
	}
	if err := os.WriteFile(i.path, []byte("new binary"), 0755); err != nil {
		return nil, err
	}
	return LocalStagedUpdate{ArtifactPath: i.path, Version: release.Metadata.Version}, nil
}

func TestManualRetryAfterAutomaticFailure(t *testing.T) {
	for _, outcome := range []string{StateFailed, StateRolledBack} {
		for _, scenario := range []string{"intact", "missing", "corrupt", "staging_failure", "expired"} {
			t.Run(outcome+"/"+scenario, func(t *testing.T) {
				now := time.Now().UTC()
				root := t.TempDir()
				client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
				target := Target{ID: "linux-amd64", Kind: "executable", OS: "linux", Arch: "amd64", URL: "https://example.test/app.tar.gz", Filetype: "tar.gz", Size: 3, SHA256: strings.Repeat("0", 64)}
				release := cachedReleaseFixture(now, target, "download")
				if err := client.saveState(persistedClientState{LastSuccessfulCheck: now, Cached: release}); err != nil {
					t.Fatal(err)
				}
				installer := &retryLocalInstaller{path: filepath.Join(root, "staged")}
				c := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0", OS: "linux", Arch: "amd64"}, Distribution: "binary"}, "stable", NewDrainManager(nil, nil, 0, nil), installer, false, "", nil)
				c.release, c.state = release, StateAvailable
				if err := c.Stage(context.Background()); err != nil {
					t.Fatal(err)
				}
				c.state, c.lastError, c.autoAttemptedVersion = outcome, "installation failed", release.Metadata.Version
				switch scenario {
				case "missing", "staging_failure":
					if err := os.Remove(installer.path); err != nil {
						t.Fatal(err)
					}
					if scenario == "staging_failure" {
						installer.stageErr = errors.New("download unavailable")
					}
				case "corrupt":
					if err := os.WriteFile(installer.path, []byte("bad binary"), 0755); err != nil {
						t.Fatal(err)
					}
				}
				// Background checks must not prepare or apply this attempted version.
				for n := 0; n < 3; n++ {
					if err := c.checkAndAutoUpdate(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				if installer.stages.Load() != 1 || installer.applies.Load() != 0 {
					t.Fatal("background check retried failed update")
				}
				if scenario == "expired" {
					now = release.Metadata.ExpiresAt.Add(time.Second)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				c.StartRecovery(ctx)
				err := c.Accept(ctx, time.Minute)
				if scenario == "staging_failure" || scenario == "expired" {
					if err == nil {
						t.Fatal("unsafe retry accepted")
					}
					c.mu.RLock()
					accepted := c.accepted
					c.mu.RUnlock()
					if accepted || installer.applies.Load() != 0 || c.drain.Status().State != DrainStateIdle {
						t.Fatal("failed preparation began installation")
					}
					if scenario == "expired" {
						return
					}
					// Once connectivity returns, the user can retry again even though
					// the failed download cleared the staged artifact.
					if c.Snapshot().Staged {
						t.Fatal("failed download retained a staged artifact")
					}
					installer.stageErr = nil
					if err := c.checkAndAutoUpdate(ctx); err != nil {
						t.Fatal(err)
					}
					if installer.stages.Load() != 2 || installer.applies.Load() != 0 {
						t.Fatal("background check retried failed download")
					}
					err = c.Accept(ctx, time.Minute)
				}
				if err != nil {
					t.Fatal(err)
				}
				waitForCoordinatorState(t, c, StateSucceeded)
				wantStages := int32(2)
				if scenario == "staging_failure" {
					wantStages = 3
				}
				if scenario == "intact" {
					wantStages = 1
				}
				if installer.stages.Load() != wantStages || installer.applies.Load() != 1 {
					t.Fatalf("stages=%d applies=%d", installer.stages.Load(), installer.applies.Load())
				}
				data, err := os.ReadFile(installer.path)
				if err != nil || string(data) != "new binary" {
					t.Fatalf("bad replacement %q: %v", data, err)
				}
				// Explicit retry does not clear the automatic-attempt guard.
				if err := c.checkAndAutoUpdate(ctx); err != nil {
					t.Fatal(err)
				}
				if installer.applies.Load() != 1 {
					t.Fatal("manual retry re-enabled automatic retry")
				}
			})
		}
	}
}

func TestInterruptedManualRetryRemainsRetryableAfterRestart(t *testing.T) {
	for _, checkpoint := range []string{StateStaging, StateChecking} {
		for _, artifact := range []string{"intact", "missing", "corrupt", "unstaged"} {
			t.Run(checkpoint+"/"+artifact, func(t *testing.T) {
				now := time.Now().UTC()
				root := t.TempDir()
				client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json")})
				target := Target{ID: "linux-amd64", Kind: "executable", OS: "linux", Arch: "amd64", URL: "https://example.test/app.tar.gz", Filetype: "tar.gz", Size: 3, SHA256: strings.Repeat("0", 64)}
				release := cachedReleaseFixture(now, target, "download")
				if err := client.saveState(persistedClientState{LastSuccessfulCheck: now, Cached: release}); err != nil {
					t.Fatal(err)
				}
				installer := &retryLocalInstaller{path: filepath.Join(root, "staged")}
				current := CurrentBuild{Build: buildinfo.Build{Version: "0.5.0", OS: "linux", Arch: "amd64"}, Distribution: "binary"}
				newCoordinator := func() *Coordinator {
					return NewCoordinator(client, current, "stable", NewDrainManager(nil, nil, 0, nil), installer, false, "", nil)
				}
				c := newCoordinator()
				statePath := filepath.Join(root, "coordinator.json")
				if err := c.SetPersistence(statePath); err != nil {
					t.Fatal(err)
				}
				c.release, c.state = release, StateAvailable
				if err := c.Stage(context.Background()); err != nil {
					t.Fatal(err)
				}
				switch artifact {
				case "missing", "unstaged":
					if err := os.Remove(installer.path); err != nil {
						t.Fatal(err)
					}
					if artifact == "unstaged" {
						c.staged = nil
					}
				case "corrupt":
					if err := os.WriteFile(installer.path, []byte("partial"), 0755); err != nil {
						t.Fatal(err)
					}
				}
				// Simulate the durable checkpoint of an interrupted retry download
				// or background check, before any new acceptance is persisted.
				c.state, c.autoAttemptedVersion = checkpoint, release.Metadata.Version
				if err := c.persistLocked(); err != nil {
					t.Fatal(err)
				}
				for restart := 0; restart < 2; restart++ {
					c = newCoordinator()
					if err := c.SetPersistence(statePath); err != nil {
						t.Fatal(err)
					}
					snapshot := c.Snapshot()
					if snapshot.State != StateFailed || snapshot.Release == nil || snapshot.Error == "" {
						t.Fatalf("restart %d lost interrupted retry: %#v", restart, snapshot)
					}
					if c.autoAttemptedVersion != release.Metadata.Version || c.accepted {
						t.Fatal("restart changed automatic-attempt guard or accepted update")
					}
					for check := 0; check < 3; check++ {
						if err := c.checkAndAutoUpdate(context.Background()); err != nil {
							t.Fatal(err)
						}
					}
					if c.Snapshot().State != StateFailed || installer.stages.Load() != 1 || installer.applies.Load() != 0 {
						t.Fatal("restart/check resumed update without approval")
					}
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				c.StartRecovery(ctx)
				if err := c.Accept(ctx, time.Minute); err != nil {
					t.Fatal(err)
				}
				waitForCoordinatorState(t, c, StateSucceeded)
				wantStages := int32(2)
				if artifact == "intact" {
					wantStages = 1
				}
				if installer.stages.Load() != wantStages || installer.applies.Load() != 1 {
					t.Fatalf("stages=%d applies=%d", installer.stages.Load(), installer.applies.Load())
				}
				data, err := os.ReadFile(installer.path)
				if err != nil || string(data) != "new binary" {
					t.Fatalf("invalid replacement %q: %v", data, err)
				}
			})
		}
	}
}
