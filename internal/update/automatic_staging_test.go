package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/buildinfo"
)

func TestDockerAutomaticUpdateStagesBeforeApplying(t *testing.T) {
	for _, supported := range []bool{true, false} {
		t.Run(map[bool]string{true: "supported", false: "unsupported"}[supported], func(t *testing.T) {
			var capabilities, applies atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v1/capabilities":
					capabilities.Add(1)
					if supported {
						_, _ = w.Write([]byte(`{"schema_version":1,"managed_updates":true}`))
					} else {
						_, _ = w.Write([]byte(`{"schema_version":1,"managed_updates":false}`))
					}
				case r.Method == http.MethodPost && r.URL.Path == "/v1/update-requests":
					if capabilities.Load() == 0 {
						t.Error("applied before checking staging capabilities")
					}
					applies.Add(1)
					w.WriteHeader(http.StatusAccepted)
					_, _ = w.Write([]byte(`{"schema_version":1,"request_id":"request-1","status":"accepted"}`))
				case r.Method == http.MethodGet && r.URL.Path == "/v1/update-requests/request-1":
					_, _ = w.Write([]byte(`{"schema_version":1,"request_id":"request-1","status":"succeeded","current_version":"0.6.0","target_version":"0.6.0"}`))
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			api, err := NewAgentHTTPClient(server.URL, "secret", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json")})
			target := Target{ID: "linux-amd64", Kind: "oci", OS: "linux", Arch: "amd64", ImageRef: "example.test/ov@sha256:" + strings.Repeat("a", 64)}
			release := cachedReleaseFixture(now, target, "container")
			if err := client.saveState(persistedClientState{LastSuccessfulCheck: now, Cached: release}); err != nil {
				t.Fatal(err)
			}
			current := CurrentBuild{Build: buildinfo.Build{Version: "0.5.0", OS: "linux", Arch: "amd64"}, Distribution: "docker"}
			drain := NewDrainManager(nil, nil, 0, nil)
			installer := &DockerAgentInstaller{API: api, Client: client, Current: current, Drain: drain, StatePath: filepath.Join(t.TempDir(), "docker.json"), PollInterval: time.Millisecond}
			c := NewCoordinator(client, current, "stable", drain, installer, false, "", nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.StartRecovery(ctx)
			err = c.checkAndAutoUpdate(ctx)
			if !supported {
				if err == nil || c.Snapshot().State != StateFailed || applies.Load() != 0 {
					t.Fatalf("unsupported agent reached apply: err=%v state=%s applies=%d", err, c.Snapshot().State, applies.Load())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			waitForCoordinatorState(t, c, StateSucceeded)
			if capabilities.Load() == 0 || applies.Load() != 1 {
				t.Fatalf("capabilities=%d applies=%d", capabilities.Load(), applies.Load())
			}
		})
	}
}

func TestAttemptedAutomaticReleaseSkipsStaging(t *testing.T) {
	for _, state := range []string{StateFailed, StateAvailable, StateRolledBack} {
		t.Run(state, func(t *testing.T) {
			now := time.Now().UTC()
			client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json")})
			target := Target{ID: "linux-amd64", Kind: "executable", OS: "linux", Arch: "amd64", URL: "https://example.test/ov.tar.gz", Filetype: "tar.gz", Size: 3, SHA256: strings.Repeat("0", 64)}
			release := cachedReleaseFixture(now, target, "download")
			if err := client.saveState(persistedClientState{LastSuccessfulCheck: now, Cached: release}); err != nil {
				t.Fatal(err)
			}
			installer := &countingInstaller{}
			c := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0", OS: "linux", Arch: "amd64"}, Distribution: "binary"}, "stable", NewDrainManager(nil, nil, 0, nil), installer, false, "", nil)
			c.autoAttemptedVersion = release.Metadata.Version
			c.release, c.state, c.lastError = release, state, "previous attempt"
			for i := 0; i < 3; i++ {
				if err := c.checkAndAutoUpdate(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if installer.stages.Load() != 0 || installer.applies.Load() != 0 {
				t.Fatal("already attempted release staged or applied again")
			}
			if c.Snapshot().State != state || c.Snapshot().Error != "previous attempt" {
				t.Fatal("previous attempt outcome lost")
			}
			// A newer release remains eligible for automatic installation.
			newer := *release
			newer.Metadata.Version = "0.7.0"
			if err := client.saveState(persistedClientState{LastSuccessfulCheck: now, Cached: &newer}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.StartRecovery(ctx)
			if err := c.checkAndAutoUpdate(ctx); err != nil {
				t.Fatal(err)
			}
			waitForCoordinatorState(t, c, StateSucceeded)
			if installer.stages.Load() != 1 || installer.applies.Load() != 1 {
				t.Fatal("new release did not stage and apply exactly once")
			}
		})
	}
}
