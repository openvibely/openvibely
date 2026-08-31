package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/buildinfo"
)

func TestHostedControllerAuthenticatesClaimsAssignedUpdateRenewsAndCancels(t *testing.T) {
	token := "hosted-secret"
	statePath := filepath.Join(t.TempDir(), "hosted.json")
	var previousVersionPersisted bool
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		paths = append(paths, r.URL.Path)
		if r.Method == http.MethodPost && r.Header.Get("Idempotency-Key") == "" {
			t.Error("missing idempotency key")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/workspace-agent/update-directive":
			_, _ = w.Write([]byte(`{"schema_version":1,"directive":"update","update_id":"assigned","desired_version":"0.6.0","policy":"when_idle","drain_lease_seconds":60,"release_notes_url":""}`))
		case strings.HasSuffix(r.URL.Path, "/ready"):
			data, err := os.ReadFile(statePath)
			if err != nil {
				t.Errorf("read durable Hosted assignment: %v", err)
			} else {
				var persisted hostedPersistentState
				if err := json.Unmarshal(data, &persisted); err != nil {
					t.Errorf("decode durable Hosted assignment: %v", err)
				} else {
					previousVersionPersisted = persisted.PreviousVersion == "0.5.0"
				}
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"schema_version":1,"accepted":true,"lease_expires_at":"2099-01-01T00:00:00Z"}`))
		case strings.HasSuffix(r.URL.Path, "/lease"):
			_, _ = w.Write([]byte(`{"schema_version":1,"state":"cancelled","lease_expires_at":"2099-01-01T00:00:00Z"}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	api, err := NewAgentHTTPClient(server.URL, token, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	drain := NewDrainManager(nil, nil, 0, time.Now)
	controller := NewHostedController(api, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, statePath)
	controller.progressInterval = time.Millisecond
	controller.renewInterval = time.Millisecond
	if err := controller.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !previousVersionPersisted {
		t.Fatal("durable Hosted assignment omitted exact previous workspace version")
	}
	if drain.Status().State != DrainStateIdle {
		t.Fatalf("cancel directive lease response did not reopen admission: %#v", drain.Status())
	}
	joined := strings.Join(paths, " ")
	for _, required := range []string{"/ready", "/lease"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("paths %q missing %s", joined, required)
		}
	}
}

func TestHostedControllerPollsCancellationWhileWorkIsActive(t *testing.T) {
	gets := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			gets++
			if gets == 1 {
				_, _ = w.Write([]byte(`{"schema_version":1,"directive":"update","update_id":"assigned","desired_version":"0.6.0","policy":"when_idle","drain_lease_seconds":60,"release_notes_url":""}`))
				return
			}
			_, _ = w.Write([]byte(`{"schema_version":1,"directive":"cancel","update_id":"assigned","desired_version":"","policy":"","drain_lease_seconds":0,"release_notes_url":""}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
	active := ActiveWork{ChatExecutions: 1}
	drain := NewDrainManager(func() ActiveWork { return active }, nil, 0, time.Now)
	controller := NewHostedController(api, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, filepath.Join(t.TempDir(), "hosted.json"))
	controller.progressInterval = time.Millisecond
	if err := controller.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gets < 2 || drain.Status().State != DrainStateIdle {
		t.Fatalf("gets=%d drain=%#v", gets, drain.Status())
	}
}

func TestHostedControllerDeclinesBusyTimeoutWhenDrainLeaseExpires(t *testing.T) {
	var decline map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/decline") {
			if err := json.NewDecoder(r.Body).Decode(&decline); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
	now := time.Now()
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	controller := NewHostedController(api, drain, CurrentBuild{}, filepath.Join(t.TempDir(), "hosted.json"))
	active := hostedPersistentState{UpdateID: "assigned", DesiredVersion: "0.6.0", DrainGeneration: status.Generation, LeaseExpiresAt: status.ExpiresAt}
	now = now.Add(2 * time.Second)
	err = controller.coordinate(context.Background(), HostedDirective{UpdateID: "assigned"}, active)
	if err == nil {
		t.Fatal("expired drain did not terminate coordination")
	}
	if decline["reason_code"] != "busy_timeout" || decline["drain_generation"] != status.Generation {
		t.Fatalf("decline=%#v", decline)
	}
}

func TestHostedStartupReconcilesSuccessfulReplacementAndRejectsUnsupportedDirective(t *testing.T) {
	now := time.Now().UTC()
	root := t.TempDir()
	statePath := filepath.Join(root, "hosted.json")
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if _, err := drain.BeginDrain(DrainRequest{Lease: time.Hour}); err != nil {
		t.Fatal(err)
	}
	status := drain.Status()
	if !drain.TakeOwnership(status.Generation) {
		t.Fatal("take Hosted drain ownership")
	}
	state := hostedPersistentState{UpdateID: "assigned", DesiredVersion: "0.6.0", DrainGeneration: status.Generation, LeaseExpiresAt: now.Add(time.Hour)}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	controller := NewHostedController(nil, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.6.0"}}, statePath)
	if err := controller.Restore(); err != nil {
		t.Fatal(err)
	}
	if drain.Status().State != DrainStateIdle {
		t.Fatal("successful Hosted replacement did not reopen admission")
	}
	if lifecycle := controller.Lifecycle(); lifecycle.State != StateSucceeded {
		t.Fatalf("successful Hosted lifecycle=%#v", lifecycle)
	}

	var decline map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"schema_version":1,"directive":"update","update_id":"unsupported","desired_version":"not-semver","policy":"immediate","drain_lease_seconds":60}`))
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&decline)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
	controller = NewHostedController(api, NewDrainManager(nil, nil, 0, nil), CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, filepath.Join(root, "other.json"))
	if err := controller.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if decline["reason_code"] != "unsupported_version" {
		t.Fatalf("decline=%#v", decline)
	}
}

func TestHostedLeaseProtocolErrorRemainsFailClosedAndReplayable(t *testing.T) {
	tests := []struct {
		name     string
		response string
	}{
		{name: "unsupported schema", response: `{"schema_version":2,"state":"draining","lease_expires_at":"2099-01-01T00:00:00Z"}`},
		{name: "invalid state", response: `{"schema_version":1,"state":"unknown","lease_expires_at":"2099-01-01T00:00:00Z"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			var firstKey atomic.Value
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				key := r.Header.Get("Idempotency-Key")
				if calls.Add(1) == 1 {
					firstKey.Store(key)
				} else if got, _ := firstKey.Load().(string); key == "" || key != got {
					t.Errorf("renewal idempotency key changed from %q to %q", got, key)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()
			api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
			drain := NewDrainManager(nil, nil, 0, time.Now)
			status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			if !drain.TakeOwnership(status.Generation) {
				t.Fatal("take Hosted drain ownership")
			}
			statePath := filepath.Join(t.TempDir(), "hosted.json")
			active := hostedPersistentState{
				UpdateID:          "assigned",
				DesiredVersion:    "0.6.0",
				Policy:            "when_idle",
				DrainGeneration:   status.Generation,
				DrainLeaseSeconds: 60,
				LeaseExpiresAt:    time.Now().Add(20 * time.Millisecond),
				Phase:             StateReady,
			}
			controller := NewHostedController(api, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, statePath)
			controller.state = active
			controller.renewInterval = time.Millisecond
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
			defer cancel()

			if err := controller.renewUntilReplacement(ctx, active); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("protocol ambiguity error=%v", err)
			}
			if calls.Load() < 2 {
				t.Fatalf("ambiguous renewal was not replayed: calls=%d", calls.Load())
			}
			if !drain.Owns(status.Generation) || drain.Admit() {
				t.Fatalf("protocol ambiguity reopened admission: lifecycle=%#v drain=%#v", controller.Lifecycle(), drain.Status())
			}
		})
	}
}

func TestHostedAmbiguousLeaseRenewalReplaysStableKeyAndAdoptsAuthoritativeLease(t *testing.T) {
	var calls atomic.Int32
	var firstKey atomic.Value
	authoritativeLease := time.Now().Add(500 * time.Millisecond).UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/lease") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		key := r.Header.Get("Idempotency-Key")
		call := calls.Add(1)
		if call == 1 {
			firstKey.Store(key)
		} else if call <= 3 {
			if got, _ := firstKey.Load().(string); key == "" || key != got {
				t.Errorf("renewal idempotency key changed from %q to %q", got, key)
			}
		}
		switch call {
		case 1:
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("response writer cannot simulate response loss")
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Fatal(err)
			}
			_ = conn.Close()
		case 2:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"schema_version":`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"schema_version":1,"state":"draining","lease_expires_at":%q}`, authoritativeLease.Format(time.RFC3339Nano))
		}
	}))
	defer server.Close()
	api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
	root := t.TempDir()
	drain := NewDrainManager(nil, nil, 0, time.Now)
	if err := drain.SetPersistence(filepath.Join(root, "drain.json")); err != nil {
		t.Fatal(err)
	}
	status, err := drain.BeginDrain(DrainRequest{Lease: 80 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if !drain.TakeOwnership(status.Generation) {
		t.Fatal("take Hosted drain ownership")
	}
	statePath := filepath.Join(root, "hosted.json")
	active := hostedPersistentState{UpdateID: "assigned", DesiredVersion: "0.6.0", Policy: "when_idle", DrainGeneration: status.Generation, DrainLeaseSeconds: 1, LeaseExpiresAt: status.ExpiresAt, Phase: StateReady, LeaseIdempotencyKey: "stable-renewal-key"}
	controller := NewHostedController(api, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, statePath)
	controller.state = active
	controller.renewInterval = 5 * time.Millisecond
	if err := controller.save(); err != nil {
		t.Fatal(err)
	}

	reconciled, terminal, err := controller.reconcileLeaseRenewal(context.Background(), active)
	if err != nil {
		t.Fatal(err)
	}
	if terminal {
		t.Fatal("draining renewal unexpectedly terminalized the operation")
	}
	if calls.Load() != 3 {
		t.Fatalf("ambiguous renewal calls=%d want 3", calls.Load())
	}
	if reconciled.LeaseIdempotencyKey != "" || !reconciled.LeaseExpiresAt.Equal(authoritativeLease) {
		t.Fatalf("reconciled renewal=%#v", reconciled)
	}
	select {
	case <-drain.Reopened():
		t.Fatal("admission reopened at the pre-renewal deadline")
	case <-time.After(120 * time.Millisecond):
	}
	if !drain.Owns(status.Generation) {
		t.Fatal("authoritative renewed lease did not retain drain ownership")
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if got, _ := persisted["LeaseExpiresAt"].(string); got != authoritativeLease.Format(time.RFC3339Nano) {
		t.Fatalf("persisted lease expiry=%q want %q", got, authoritativeLease.Format(time.RFC3339Nano))
	}
	if got, _ := persisted["LeaseIdempotencyKey"].(string); got != "" {
		t.Fatalf("persisted renewal key was not cleared: %q", got)
	}
}

func TestHostedRestartReplaysPendingLeaseRenewalPastPreviousDeadline(t *testing.T) {
	var calls atomic.Int32
	var firstKey atomic.Value
	var keyMismatch atomic.Bool
	authoritativeLease := time.Now().Add(5 * time.Second).UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/lease") {
			key := r.Header.Get("Idempotency-Key")
			if calls.Add(1) == 1 {
				firstKey.Store(key)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if got, _ := firstKey.Load().(string); key != got {
				keyMismatch.Store(true)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"schema_version":1,"state":"draining","lease_expires_at":%q}`, authoritativeLease.Format(time.RFC3339Nano))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
	root := t.TempDir()
	drainPath := filepath.Join(root, "drain.json")
	drain := NewDrainManager(nil, nil, 0, time.Now)
	if err := drain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !drain.TakeOwnership(status.Generation) {
		t.Fatal("take Hosted drain ownership")
	}
	statePath := filepath.Join(root, "hosted.json")
	previousDeadline := time.Now().Add(-time.Second).UTC()
	data := []byte(fmt.Sprintf(`{"UpdateID":"assigned","DesiredVersion":"0.6.0","Policy":"when_idle","DrainGeneration":%q,"DrainLeaseSeconds":1,"LeaseExpiresAt":%q,"Phase":"ready","LeaseIdempotencyKey":"stable-renewal-key"}`, status.Generation, previousDeadline.Format(time.RFC3339Nano)))
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	restoredDrain := NewDrainManager(nil, nil, 0, time.Now)
	if err := restoredDrain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	controller := NewHostedController(api, restoredDrain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, statePath)
	controller.renewInterval = 5 * time.Millisecond
	if err := controller.Restore(); err != nil {
		t.Fatalf("restore pending renewal: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		controller.mu.Lock()
		active := controller.state
		controller.mu.Unlock()
		done <- controller.renewUntilReplacement(ctx, active)
	}()
	deadline := time.Now().Add(time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() < 2 {
		cancel()
		t.Fatalf("pending renewal was not replayed after restart: calls=%d", calls.Load())
	}
	for time.Now().Before(deadline) {
		controller.mu.Lock()
		pendingKey := controller.state.LeaseIdempotencyKey
		controller.mu.Unlock()
		if pendingKey == "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("renewal shutdown error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("renewal replay did not stop with context cancellation")
	}
	if got, _ := firstKey.Load().(string); got != "stable-renewal-key" || keyMismatch.Load() {
		t.Fatalf("restart renewal key=%q mismatch=%v", got, keyMismatch.Load())
	}
	if !restoredDrain.Owns(status.Generation) {
		t.Fatal("restart released drain at the previous lease deadline")
	}
}

func TestHostedLeasePersistenceFailureRemainsSupervisedUntilExpiry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":1,"state":"draining","lease_expires_at":"2099-01-01T00:00:00Z"}`))
	}))
	defer server.Close()
	api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
	drain := NewDrainManager(nil, nil, 0, time.Now)
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !drain.TakeOwnership(status.Generation) {
		t.Fatal("take Hosted drain ownership")
	}
	statePath := filepath.Join(t.TempDir(), "hosted.json")
	active := hostedPersistentState{
		UpdateID:          "assigned",
		DesiredVersion:    "0.6.0",
		Policy:            "when_idle",
		DrainGeneration:   status.Generation,
		DrainLeaseSeconds: 60,
		LeaseExpiresAt:    time.Now().Add(40 * time.Millisecond),
		Phase:             StateReady,
	}
	controller := NewHostedController(api, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, statePath)
	controller.state = active
	controller.renewInterval = time.Millisecond
	var writes atomic.Int32
	controller.stateWriter = func(path string, data []byte) error {
		if writes.Add(1) <= 2 {
			return errors.New("transient Hosted state write failure")
		}
		return atomicWriteState(path, data)
	}
	started := time.Now()

	if err := controller.renewUntilReplacement(context.Background(), active); err == nil {
		t.Fatal("lease persistence failure unexpectedly succeeded")
	}
	if elapsed := time.Since(started); elapsed < 20*time.Millisecond {
		t.Fatalf("lease supervision returned before last durable expiry: %v", elapsed)
	}
	if writes.Load() < 3 {
		t.Fatalf("durable cancellation was not retried after persistence failure: writes=%d", writes.Load())
	}
	if drain.Status().State != DrainStateIdle || controller.Lifecycle().Active {
		t.Fatalf("persistence-error operation remained active: lifecycle=%#v drain=%#v", controller.Lifecycle(), drain.Status())
	}
}

func TestDockerAgentRequestContainsOnlyReadinessMetadataAndPersistsStatus(t *testing.T) {
	token := "docker-secret"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing bearer token")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/update-requests":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			for _, forbidden := range []string{"image", "image_ref", "command", "path", "compose_path", "volume", "health_url", "url"} {
				if _, ok := body[forbidden]; ok {
					t.Errorf("forbidden field %s", forbidden)
				}
			}
			for _, required := range []string{"schema_version", "requested_version", "current_version", "drain_generation", "active_total"} {
				if _, ok := body[required]; !ok {
					t.Errorf("missing field %s", required)
				}
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"schema_version":1,"request_id":"request-1","status":"accepted"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/capabilities":
			_, _ = w.Write([]byte(`{"schema_version":1,"managed_updates":true}`))
		case r.Method == http.MethodGet:
			calls++
			status := "accepted"
			currentVersion := "0.5.0"
			if calls > 1 {
				status = "succeeded"
				currentVersion = "0.6.0"
			}
			_, _ = w.Write([]byte(`{"schema_version":1,"request_id":"request-1","status":"` + status + `","current_version":"` + currentVersion + `","target_version":"0.6.0","failure_code":""}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	api, err := NewAgentHTTPClient(server.URL, token, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json"), Now: func() time.Time { return now }})
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	_, _ = drain.BeginDrain(DrainRequest{Lease: time.Minute})
	_ = drain.Status()
	installer := &DockerAgentInstaller{API: api, Client: client, Current: CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, Drain: drain, StatePath: filepath.Join(t.TempDir(), "docker.json"), PollInterval: time.Millisecond}
	release := VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}, Target: Target{Kind: "oci", ImageRef: "ghcr.io/openvibely/openvibely@sha256:" + strings.Repeat("a", 64)}}
	staged, err := installer.Stage(context.Background(), release)
	if err != nil {
		t.Fatal(err)
	}
	if err := installer.Apply(context.Background(), staged); err != nil {
		t.Fatal(err)
	}
	if err := installer.Validate(context.Background(), release.Metadata); err != nil {
		t.Fatal(err)
	}
	restored := &DockerAgentInstaller{StatePath: installer.StatePath}
	if err := restored.Load(); err != nil {
		t.Fatal(err)
	}
	if restored.state.RequestID != "request-1" || restored.state.Status != "succeeded" {
		t.Fatalf("restored state=%#v", restored.state)
	}
}

func TestDockerAgentRejectsWrongReportedVersionAndResumesPersistedRequest(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":1,"request_id":"request-1","status":"succeeded","current_version":"0.5.0","target_version":"0.7.0","failure_code":""}`))
	}))
	defer server.Close()
	api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
	installer := &DockerAgentInstaller{API: api, StatePath: filepath.Join(t.TempDir(), "docker.json"), PollInterval: time.Millisecond, state: dockerPersistentState{RequestID: "request-1", Status: "replacing", CurrentVersion: "0.5.0", TargetVersion: "0.6.0", DrainGeneration: "generation"}}
	if err := installer.save(); err != nil {
		t.Fatal(err)
	}
	restored := &DockerAgentInstaller{API: api, StatePath: installer.StatePath, PollInterval: time.Millisecond}
	if err := restored.Load(); err != nil {
		t.Fatal(err)
	}
	err := restored.Resume(context.Background())
	if !errors.Is(err, ErrUpdateRecoveryPending) {
		t.Fatalf("wrong agent-reported target/current version must remain fail-closed and recoverable, got %v", err)
	}
	if restored.state.Status != "succeeded" || restored.state.ReportedCurrentVersion != "0.5.0" || restored.state.ReportedTargetVersion != "0.7.0" {
		t.Fatalf("mismatched replacement identity was not durably retained: %#v", restored.state)
	}
	if calls == 0 {
		t.Fatal("persisted Docker request was not resumed")
	}
}

func TestDockerAgentVersionMismatchRemainsOwnedForReconciliation(t *testing.T) {
	var statusCalls atomic.Int32
	secondStatus := make(chan struct{})
	allowCancellation := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/update-requests":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"schema_version":1,"request_id":"request-1","status":"accepted"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/update-requests/request-1":
			if statusCalls.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"schema_version":1,"request_id":"request-1","status":"succeeded","current_version":"0.5.0","target_version":"0.7.0"}`))
				return
			}
			close(secondStatus)
			<-allowCancellation
			_, _ = w.Write([]byte(`{"schema_version":1,"request_id":"request-1","status":"cancelled","current_version":"0.5.0","target_version":"0.7.0"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	now := time.Now()
	api, err := NewAgentHTTPClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json"), Now: func() time.Time { return now }})
	installer := &DockerAgentInstaller{API: api, Client: client, Current: CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, Drain: drain, StatePath: filepath.Join(t.TempDir(), "docker.json"), PollInterval: time.Millisecond}
	release := VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}, Target: Target{Kind: "oci", ImageRef: "ghcr.io/openvibely/openvibely@sha256:" + strings.Repeat("a", 64)}}
	coordinator := NewCoordinator(client, installer.Current, "stable", drain, installer, false, "", nil)
	coordinator.recoveryRetryInterval = time.Millisecond
	coordinator.release = &release
	coordinator.staged = release
	coordinator.state = StateAvailable
	if err := coordinator.Apply(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		select {
		case <-secondStatus:
			snapshot := coordinator.Snapshot()
			if snapshot.State != StateApplying || !drain.Owns(snapshot.Drain.Generation) {
				t.Fatalf("mismatched replacement did not remain fail-closed: %#v", snapshot)
			}
			close(allowCancellation)
			for coordinator.Snapshot().State != StateIdle && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if snapshot := coordinator.Snapshot(); snapshot.State != StateIdle || snapshot.Drain.State != DrainStateIdle {
				t.Fatalf("definitive agent cancellation did not settle operation: %#v", snapshot)
			}
			return
		default:
			snapshot := coordinator.Snapshot()
			if snapshot.State == StateFailed || snapshot.Drain.State == DrainStateIdle {
				t.Fatalf("version mismatch triggered rollback cleanup: %#v", snapshot)
			}
			if time.Now().After(deadline) {
				t.Fatal("mismatched succeeded status was not reconciled")
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func TestHostedRestoreOldVersionAfterReplacementStartedReleasesRolledBackDrain(t *testing.T) {
	now := time.Now().UTC()
	root := t.TempDir()
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !drain.TakeOwnership(status.Generation) {
		t.Fatal("take Hosted drain ownership")
	}
	statePath := filepath.Join(root, "hosted.json")
	state := hostedPersistentState{UpdateID: "assigned", DesiredVersion: "0.6.0", PreviousVersion: "0.5.0", DrainGeneration: status.Generation, LeaseExpiresAt: now.Add(time.Hour), Phase: StateRestarting}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	controller := NewHostedController(nil, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, statePath)
	if err := controller.Restore(); err != nil {
		t.Fatal(err)
	}
	if snapshot := controller.Lifecycle(); snapshot.State != StateRolledBack || drain.Status().State != DrainStateIdle {
		t.Fatalf("lifecycle=%#v drain=%#v", snapshot, drain.Status())
	}
}

func TestHostedRestoreUnexpectedLowerVersionKeepsDrainClosed(t *testing.T) {
	now := time.Now().UTC()
	root := t.TempDir()
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !drain.TakeOwnership(status.Generation) {
		t.Fatal("take Hosted drain ownership")
	}
	statePath := filepath.Join(root, "hosted.json")
	state := map[string]any{
		"UpdateID":         "assigned",
		"DesiredVersion":   "0.6.0",
		"previous_version": "0.5.0",
		"DrainGeneration":  status.Generation,
		"LeaseExpiresAt":   now.Add(time.Hour),
		"Phase":            StateRestarting,
	}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	controller := NewHostedController(nil, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.4.0"}}, statePath)
	if err := controller.Restore(); err == nil {
		t.Fatal("unexpected lower replacement version accepted as rollback")
	}
	if drain.Status().State == DrainStateIdle || controller.Lifecycle().State != StateRestarting {
		t.Fatalf("lifecycle=%#v drain=%#v", controller.Lifecycle(), drain.Status())
	}
}

func TestHostedRestoreUnexpectedReplacementVersionKeepsDrainClosed(t *testing.T) {
	now := time.Now().UTC()
	root := t.TempDir()
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !drain.TakeOwnership(status.Generation) {
		t.Fatal("take Hosted drain ownership")
	}
	statePath := filepath.Join(root, "hosted.json")
	state := hostedPersistentState{UpdateID: "assigned", DesiredVersion: "0.6.0", PreviousVersion: "0.5.0", DrainGeneration: status.Generation, LeaseExpiresAt: now.Add(time.Hour), Phase: StateRestarting}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	controller := NewHostedController(nil, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.7.0"}}, statePath)
	if err := controller.Restore(); err == nil {
		t.Fatal("unexpected replacement version accepted")
	}
	if drain.Status().State == DrainStateIdle || controller.Lifecycle().State != StateRestarting {
		t.Fatalf("lifecycle=%#v drain=%#v", controller.Lifecycle(), drain.Status())
	}
}

func TestHostedRestoreExpiresOwnedReadyLeaseBeforeRenewal(t *testing.T) {
	now := time.Now().UTC()
	var leaseCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/lease") {
			leaseCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"schema_version":1,"state":"draining","lease_expires_at":"2099-01-01T00:00:00Z"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
	root := t.TempDir()
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := drain.SetPersistence(filepath.Join(root, "drain.json")); err != nil {
		t.Fatal(err)
	}
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !drain.TakeOwnership(status.Generation) {
		t.Fatal("take Hosted drain ownership")
	}
	statePath := filepath.Join(root, "hosted.json")
	state := hostedPersistentState{UpdateID: "assigned", DesiredVersion: "0.6.0", Policy: "when_idle", DrainGeneration: status.Generation, DrainLeaseSeconds: 60, LeaseExpiresAt: now.Add(-time.Second), Phase: StateReady}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	controller := NewHostedController(api, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, statePath)
	controller.now = func() time.Time { return now }
	controller.renewInterval = time.Millisecond
	if err := controller.Restore(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	controller.Start(ctx)
	time.Sleep(10 * time.Millisecond)
	cancel()

	if calls := leaseCalls.Load(); calls != 0 {
		t.Fatalf("expired Hosted lease was renewed %d times", calls)
	}
	if drain.Status().State != DrainStateIdle || controller.Lifecycle().Active {
		t.Fatalf("expired restored operation remained active: lifecycle=%#v drain=%#v", controller.Lifecycle(), drain.Status())
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("Hosted state file still exists after expiry: %v", err)
	}
}

func TestHostedOwnedReadyRestartResumesLeaseWithoutSecondReadinessClaim(t *testing.T) {
	readyCalls, leaseCalls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"schema_version":1,"directive":"update","update_id":"assigned","desired_version":"0.6.0","policy":"when_idle","drain_lease_seconds":60}`))
		case strings.HasSuffix(r.URL.Path, "/ready"):
			readyCalls++
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"schema_version":1,"accepted":true,"lease_expires_at":"2099-01-01T00:00:00Z"}`))
		case strings.HasSuffix(r.URL.Path, "/lease"):
			leaseCalls++
			_, _ = w.Write([]byte(`{"schema_version":1,"state":"cancelled","lease_expires_at":"2099-01-01T00:00:00Z"}`))
		}
	}))
	defer server.Close()
	api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
	drain := NewDrainManager(nil, nil, 0, time.Now)
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	_ = drain.Status()
	if !drain.TakeOwnership(status.Generation) {
		t.Fatal("take Hosted drain ownership")
	}
	controller := NewHostedController(api, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, filepath.Join(t.TempDir(), "hosted.json"))
	controller.renewInterval = time.Millisecond
	controller.state = hostedPersistentState{UpdateID: "assigned", DesiredVersion: "0.6.0", DrainGeneration: status.Generation, LeaseExpiresAt: time.Now().Add(time.Hour), Phase: StateReady}
	if err := controller.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if readyCalls != 0 || leaseCalls == 0 || drain.Status().State != DrainStateIdle {
		t.Fatalf("ready=%d lease=%d drain=%#v", readyCalls, leaseCalls, drain.Status())
	}
}

func TestHostedCancellationAfterReplacementStartedKeepsAdmissionClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":1,"directive":"cancel","update_id":"assigned"}`))
	}))
	defer server.Close()
	api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
	drain := NewDrainManager(nil, nil, 0, time.Now)
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !drain.TakeOwnership(status.Generation) {
		t.Fatal("take Hosted drain ownership")
	}
	controller := NewHostedController(api, drain, CurrentBuild{}, filepath.Join(t.TempDir(), "hosted.json"))
	controller.state = hostedPersistentState{UpdateID: "assigned", DesiredVersion: "0.6.0", DrainGeneration: status.Generation, Phase: StateRestarting}
	if err := controller.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if drain.Status().State == DrainStateIdle || controller.Lifecycle().State != StateRestarting {
		t.Fatalf("lifecycle=%#v drain=%#v", controller.Lifecycle(), drain.Status())
	}
}

func TestDockerAgentResumeReportsAcceptedCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":1,"request_id":"request-1","status":"cancelled","current_version":"0.5.0","target_version":"0.6.0","failure_code":""}`))
	}))
	defer server.Close()
	api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
	installer := &DockerAgentInstaller{API: api, PollInterval: time.Millisecond, state: dockerPersistentState{RequestID: "request-1", Status: "accepted", CurrentVersion: "0.5.0", TargetVersion: "0.6.0"}}
	if err := installer.Resume(context.Background()); !errors.Is(err, ErrUpdateCancelled) {
		t.Fatalf("resume error=%v", err)
	}
}

func TestHostedNoDirectiveReconcilesRestoredWaitingDrainExpiry(t *testing.T) {
	now := time.Now().UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/workspace-agent/update-directive":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/decline"):
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "hosted.json")
	controller := NewHostedController(api, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, statePath)
	controller.state = hostedPersistentState{UpdateID: "assigned", DesiredVersion: "0.6.0", Policy: "when_idle", DrainGeneration: status.Generation, LeaseExpiresAt: status.ExpiresAt, Phase: StateWaitingForIdle}
	if err := controller.save(); err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Second)
	if err := controller.poll(context.Background()); err == nil {
		t.Fatal("expired restored Hosted operation unexpectedly succeeded")
	}
	if drain.Status().State != DrainStateIdle || controller.Lifecycle().Active {
		t.Fatalf("expired restored operation was not cleared: lifecycle=%#v drain=%#v", controller.Lifecycle(), drain.Status())
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("Hosted state file still exists after expiry: %v", err)
	}
}

func TestHostedNoDirectiveReplaysRestoredClaimingReady(t *testing.T) {
	readyCalls, leaseCalls := 0, 0
	var readyKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/workspace-agent/update-directive":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/ready"):
			readyCalls++
			readyKey = r.Header.Get("Idempotency-Key")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"schema_version":1,"accepted":true,"lease_expires_at":"2099-01-01T00:00:00Z"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/lease"):
			leaseCalls++
			_, _ = w.Write([]byte(`{"schema_version":1,"state":"cancelled","lease_expires_at":"2099-01-01T00:00:00Z"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
	drain := NewDrainManager(nil, nil, 0, time.Now)
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	_ = drain.Status()
	_ = drain.Status()
	if !drain.TakeOwnership(status.Generation) {
		t.Fatal("take Hosted drain ownership")
	}
	controller := NewHostedController(api, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, filepath.Join(t.TempDir(), "hosted.json"))
	controller.renewInterval = time.Millisecond
	controller.state = hostedPersistentState{UpdateID: "assigned", DesiredVersion: "0.6.0", Policy: "when_idle", DrainGeneration: status.Generation, LeaseExpiresAt: time.Now().Add(time.Hour), Phase: hostedPhaseClaimingReady, ReadyIdempotencyKey: "durable-ready-key"}
	if err := controller.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if readyCalls != 1 || leaseCalls == 0 || readyKey != "durable-ready-key" {
		t.Fatalf("ready calls=%d lease calls=%d ready key=%q", readyCalls, leaseCalls, readyKey)
	}
	if drain.Status().State != DrainStateIdle || controller.Lifecycle().Active {
		t.Fatalf("cancelled recovered operation remained active: lifecycle=%#v drain=%#v", controller.Lifecycle(), drain.Status())
	}
}

func TestHostedNoDirectiveReplaysExpiredClaimUntilDefinitiveRejection(t *testing.T) {
	now := time.Now().UTC()
	readyCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/workspace-agent/update-directive":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/ready"):
			readyCalls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"schema_version":1,"accepted":false,"lease_expires_at":"0001-01-01T00:00:00Z"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_ = drain.Status()
	_ = drain.Status()
	if !drain.TakeOwnership(status.Generation) {
		t.Fatal("take Hosted drain ownership")
	}
	controller := NewHostedController(api, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, filepath.Join(t.TempDir(), "hosted.json"))
	controller.now = func() time.Time { return now }
	controller.state = hostedPersistentState{UpdateID: "assigned", DesiredVersion: "0.6.0", Policy: "when_idle", DrainGeneration: status.Generation, LeaseExpiresAt: status.ExpiresAt, Phase: hostedPhaseClaimingReady, ReadyIdempotencyKey: "durable-ready-key"}

	now = now.Add(2 * time.Second)
	if err := controller.poll(context.Background()); err == nil {
		t.Fatal("definitively rejected claiming-ready recovery unexpectedly succeeded")
	}
	if readyCalls != 1 || drain.Status().State != DrainStateIdle || controller.Lifecycle().Active {
		t.Fatalf("ready calls=%d lifecycle=%#v drain=%#v", readyCalls, controller.Lifecycle(), drain.Status())
	}
}

func TestHostedConflictingSameIDDirectiveHasNoControlPlaneSideEffects(t *testing.T) {
	tests := []struct {
		name      string
		directive string
	}{
		{
			name:      "unsupported policy",
			directive: `{"schema_version":1,"directive":"update","update_id":"assigned","desired_version":"0.6.0","policy":"immediate","drain_lease_seconds":60}`,
		},
		{
			name:      "current desired version",
			directive: `{"schema_version":1,"directive":"update","update_id":"assigned","desired_version":"0.5.0","policy":"when_idle","drain_lease_seconds":60}`,
		},
		{
			name:      "older desired version",
			directive: `{"schema_version":1,"directive":"update","update_id":"assigned","desired_version":"0.4.0","policy":"when_idle","drain_lease_seconds":60}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			postCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(tt.directive))
					return
				}
				postCalls++
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
			drain := NewDrainManager(func() ActiveWork { return ActiveWork{TaskExecutions: 1} }, nil, 0, time.Now)
			status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			active := hostedPersistentState{UpdateID: "assigned", DesiredVersion: "0.6.0", Policy: "when_idle", DrainGeneration: status.Generation, DrainLeaseSeconds: 60, LeaseExpiresAt: status.ExpiresAt, Phase: StateWaitingForIdle}
			controller := NewHostedController(api, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, filepath.Join(t.TempDir(), "hosted.json"))
			controller.state = active

			if err := controller.poll(context.Background()); err == nil {
				t.Fatal("conflicting repeated directive was accepted")
			}
			if postCalls != 0 {
				t.Fatalf("conflicting repeated directive caused %d control-plane side effects", postCalls)
			}
			if drain.Status().State == DrainStateIdle {
				t.Fatal("conflicting repeated directive reopened admission")
			}
			if lifecycle := controller.Lifecycle(); lifecycle.DesiredVersion != active.DesiredVersion || lifecycle.State != active.Phase {
				t.Fatalf("durable assignment changed: %#v", lifecycle)
			}
		})
	}
}

func TestHostedRepeatedDirectiveMustMatchDurableAssignment(t *testing.T) {
	tests := []struct {
		name      string
		active    hostedPersistentState
		directive string
	}{
		{
			name:      "desired version",
			active:    hostedPersistentState{DesiredVersion: "0.6.0", Policy: "when_idle", ReleaseNotesURL: "https://example.invalid/releases/0.6.0"},
			directive: `{"schema_version":1,"directive":"update","update_id":"assigned","desired_version":"0.7.0","policy":"when_idle","drain_lease_seconds":60,"release_notes_url":"https://example.invalid/releases/0.6.0"}`,
		},
		{
			name:      "policy",
			active:    hostedPersistentState{DesiredVersion: "0.6.0", Policy: "different-policy", ReleaseNotesURL: "https://example.invalid/releases/0.6.0"},
			directive: `{"schema_version":1,"directive":"update","update_id":"assigned","desired_version":"0.6.0","policy":"when_idle","drain_lease_seconds":60,"release_notes_url":"https://example.invalid/releases/0.6.0"}`,
		},
		{
			name:      "drain lease metadata",
			active:    hostedPersistentState{DesiredVersion: "0.6.0", Policy: "when_idle", DrainLeaseSeconds: 300, ReleaseNotesURL: "https://example.invalid/releases/0.6.0"},
			directive: `{"schema_version":1,"directive":"update","update_id":"assigned","desired_version":"0.6.0","policy":"when_idle","drain_lease_seconds":60,"release_notes_url":"https://example.invalid/releases/0.6.0"}`,
		},
		{
			name:      "release metadata",
			active:    hostedPersistentState{DesiredVersion: "0.6.0", Policy: "when_idle", ReleaseNotesURL: "https://example.invalid/releases/original"},
			directive: `{"schema_version":1,"directive":"update","update_id":"assigned","desired_version":"0.6.0","policy":"when_idle","drain_lease_seconds":60,"release_notes_url":"https://example.invalid/releases/changed"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readyCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet:
					_, _ = w.Write([]byte(tt.directive))
				case strings.HasSuffix(r.URL.Path, "/ready"):
					readyCalls++
					w.WriteHeader(http.StatusAccepted)
					_, _ = w.Write([]byte(`{"schema_version":1,"accepted":true,"lease_expires_at":"2099-01-01T00:00:00Z"}`))
				case strings.HasSuffix(r.URL.Path, "/lease"):
					_, _ = w.Write([]byte(`{"schema_version":1,"state":"cancelled","lease_expires_at":"2099-01-01T00:00:00Z"}`))
				default:
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			defer server.Close()
			api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
			drain := NewDrainManager(nil, nil, 0, time.Now)
			status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			_ = drain.Status()
			_ = drain.Status()
			controller := NewHostedController(api, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, filepath.Join(t.TempDir(), "hosted.json"))
			tt.active.UpdateID = "assigned"
			tt.active.DrainGeneration = status.Generation
			tt.active.LeaseExpiresAt = status.ExpiresAt
			tt.active.Phase = StateWaitingForIdle
			controller.state = tt.active
			controller.renewInterval = time.Millisecond

			if err := controller.poll(context.Background()); err == nil {
				t.Fatal("conflicting repeated directive was accepted")
			}
			if readyCalls != 0 || drain.Status().State == DrainStateIdle {
				t.Fatalf("conflicting directive changed active operation: ready calls=%d drain=%#v", readyCalls, drain.Status())
			}
			if lifecycle := controller.Lifecycle(); lifecycle.DesiredVersion != tt.active.DesiredVersion || lifecycle.ReleaseNotesURL != tt.active.ReleaseNotesURL {
				t.Fatalf("durable assignment changed: %#v", lifecycle)
			}
		})
	}
}

func TestHostedInFlightRepeatedDirectiveMustMatchDurableAssignment(t *testing.T) {
	gets := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet:
			gets++
			if gets == 1 {
				_, _ = w.Write([]byte(`{"schema_version":1,"directive":"update","update_id":"assigned","desired_version":"0.6.0","policy":"when_idle","drain_lease_seconds":60,"release_notes_url":"https://example.invalid/releases/0.6.0"}`))
				return
			}
			if gets == 2 {
				_, _ = w.Write([]byte(`{"schema_version":1,"directive":"update","update_id":"assigned","desired_version":"0.7.0","policy":"when_idle","drain_lease_seconds":60,"release_notes_url":"https://example.invalid/releases/0.6.0"}`))
				return
			}
			_, _ = w.Write([]byte(`{"schema_version":1,"directive":"cancel","update_id":"assigned"}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
	drain := NewDrainManager(func() ActiveWork { return ActiveWork{TaskExecutions: 1} }, nil, 0, time.Now)
	controller := NewHostedController(api, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, filepath.Join(t.TempDir(), "hosted.json"))
	controller.progressInterval = time.Millisecond

	if err := controller.poll(context.Background()); err == nil {
		t.Fatal("conflicting in-flight repeated directive was accepted")
	}
	if gets != 2 || drain.Status().State == DrainStateIdle {
		t.Fatalf("gets=%d drain=%#v", gets, drain.Status())
	}
	if lifecycle := controller.Lifecycle(); lifecycle.DesiredVersion != "0.6.0" || lifecycle.ReleaseNotesURL != "https://example.invalid/releases/0.6.0" {
		t.Fatalf("durable assignment changed: %#v", lifecycle)
	}
}

func TestHostedRestartClaimingReadyReplaysSameClaim(t *testing.T) {
	var readyCalls atomic.Int32
	var firstKey atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/ready"):
			key := r.Header.Get("Idempotency-Key")
			if readyCalls.Add(1) == 1 {
				firstKey.Store(key)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if got, _ := firstKey.Load().(string); key == "" || key != got {
				t.Errorf("readiness idempotency key changed from %q to %q", got, key)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"schema_version":1,"accepted":true,"lease_expires_at":"2099-01-01T00:00:00Z"}`))
		case strings.HasSuffix(r.URL.Path, "/lease"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"schema_version":1,"state":"cancelled","lease_expires_at":"2099-01-01T00:00:00Z"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/workspace-agent/update-directive":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected control-plane request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	api, err := NewAgentHTTPClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	drainPath := filepath.Join(root, "drain.json")
	statePath := filepath.Join(root, "hosted.json")
	oldDrain := NewDrainManager(nil, nil, 0, time.Now)
	if err := oldDrain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	status, err := oldDrain.BeginDrain(DrainRequest{Lease: 150 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if !oldDrain.TakeOwnership(status.Generation) {
		t.Fatal("persist claiming-ready drain ownership")
	}
	state := hostedPersistentState{
		UpdateID:            "assigned",
		DesiredVersion:      "0.6.0",
		Policy:              "when_idle",
		DrainGeneration:     status.Generation,
		DrainLeaseSeconds:   1,
		LeaseExpiresAt:      status.ExpiresAt,
		Phase:               hostedPhaseClaimingReady,
		ReadyIdempotencyKey: "durable-ready-key",
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteState(statePath, data); err != nil {
		t.Fatal(err)
	}

	restartedDrain := NewDrainManager(nil, nil, 0, time.Now)
	if err := restartedDrain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	controller := NewHostedController(api, restartedDrain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, statePath)
	controller.renewInterval = 5 * time.Millisecond
	if err := controller.Restore(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	controller.Start(ctx)

	select {
	case <-restartedDrain.Reopened():
	case <-time.After(time.Second):
		t.Fatalf("claiming-ready restart was not reconciled; readyCalls=%d", readyCalls.Load())
	}
	if readyCalls.Load() != 2 || controller.Lifecycle().Active {
		t.Fatalf("readyCalls=%d lifecycle=%#v", readyCalls.Load(), controller.Lifecycle())
	}
	persisted := NewDrainManager(nil, nil, 0, time.Now)
	if err := persisted.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	if !persisted.Admit() {
		t.Fatal("reconciled claiming-ready drain did not durably reopen")
	}
}

func TestHostedRestartWithoutAssignmentStateAutonomouslyExpiresDrain(t *testing.T) {
	root := t.TempDir()
	drainPath := filepath.Join(root, "drain.json")
	oldDrain := NewDrainManager(nil, nil, 0, time.Now)
	if err := oldDrain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	if _, err := oldDrain.BeginDrain(DrainRequest{Lease: 150 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}

	restartedDrain := NewDrainManager(nil, nil, 0, time.Now)
	if err := restartedDrain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	restartedDrain.supervisorInterval = 5 * time.Millisecond
	controller := NewHostedController(nil, restartedDrain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, filepath.Join(root, "missing-hosted.json"))
	if err := controller.Restore(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restartedDrain.StartExpirySupervisor(ctx)

	select {
	case <-restartedDrain.Reopened():
	case <-time.After(time.Second):
		t.Fatal("Hosted drain without assignment state did not expire autonomously after restart")
	}
	persisted := NewDrainManager(nil, nil, 0, time.Now)
	if err := persisted.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	if !persisted.Admit() {
		t.Fatal("Hosted orphan drain expiry was not durable")
	}
}

func TestHostedRestoreWithoutAssignmentStateReleasesOwnedDrain(t *testing.T) {
	root := t.TempDir()
	drainPath := filepath.Join(root, "drain.json")
	oldDrain := NewDrainManager(nil, nil, 0, time.Now)
	if err := oldDrain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	status, err := oldDrain.BeginDrain(DrainRequest{Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if !oldDrain.TakeOwnership(status.Generation) {
		t.Fatal("take orphan drain ownership")
	}

	restartedDrain := NewDrainManager(nil, nil, 0, time.Now)
	if err := restartedDrain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	controller := NewHostedController(nil, restartedDrain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, filepath.Join(root, "missing-hosted.json"))
	if err := controller.Restore(); err != nil {
		t.Fatal(err)
	}
	if !restartedDrain.Admit() {
		t.Fatal("owned orphan Hosted drain was not released")
	}

	persisted := NewDrainManager(nil, nil, 0, time.Now)
	if err := persisted.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	if !persisted.Admit() {
		t.Fatal("owned orphan Hosted drain release was not durable")
	}
}

func TestHostedReadyClaimReplaysDurableIdempotencyAfterAcceptedResponseCrash(t *testing.T) {
	var readyKeys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"schema_version":1,"directive":"update","update_id":"assigned","desired_version":"0.6.0","policy":"when_idle","drain_lease_seconds":60}`))
		case strings.HasSuffix(r.URL.Path, "/ready"):
			readyKeys = append(readyKeys, r.Header.Get("Idempotency-Key"))
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"schema_version":1,"accepted":true,"lease_expires_at":"2099-01-01T00:00:00Z"}`))
		case strings.HasSuffix(r.URL.Path, "/lease"):
			_, _ = w.Write([]byte(`{"schema_version":1,"state":"cancelled","lease_expires_at":"2099-01-01T00:00:00Z"}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
	root := t.TempDir()
	drain := NewDrainManager(nil, nil, 0, time.Now)
	if err := drain.SetPersistence(filepath.Join(root, "drain.json")); err != nil {
		t.Fatal(err)
	}
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	_ = drain.Status()
	_ = drain.Status()
	statePath := filepath.Join(root, "hosted.json")
	state := hostedPersistentState{UpdateID: "assigned", DesiredVersion: "0.6.0", DrainGeneration: status.Generation, LeaseExpiresAt: status.ExpiresAt, Phase: StateWaitingForIdle}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	writes := 0
	crashCtx, cancelCrash := context.WithCancel(context.Background())
	defer cancelCrash()
	controller := NewHostedController(api, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, statePath)
	controller.state = state
	controller.renewInterval = time.Millisecond
	controller.stateWriter = func(path string, data []byte) error {
		writes++
		if writes >= 2 {
			cancelCrash()
			return errors.New("crash after ready accepted")
		}
		return atomicWriteState(path, data)
	}
	if err := controller.coordinate(crashCtx, HostedDirective{UpdateID: "assigned"}, state); err == nil {
		t.Fatal("ready handoff persistence failure was ignored")
	}
	restored := NewHostedController(api, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, statePath)
	restored.renewInterval = time.Millisecond
	if err := restored.Restore(); err != nil {
		t.Fatal(err)
	}
	if err := restored.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(readyKeys) != 2 || readyKeys[0] == "" || readyKeys[0] != readyKeys[1] {
		t.Fatalf("ready idempotency keys=%q", readyKeys)
	}
}

func TestHostedInitialAssignmentPersistenceFailureRetriesDrainCleanupAutonomously(t *testing.T) {
	var directiveGets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		directiveGets.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":1,"directive":"update","update_id":"assigned","desired_version":"0.6.0","policy":"when_idle","drain_lease_seconds":60}`))
	}))
	defer server.Close()
	api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
	root := t.TempDir()
	drain := NewDrainManager(nil, nil, 0, time.Now)
	if err := drain.SetPersistence(filepath.Join(root, "drain.json")); err != nil {
		t.Fatal(err)
	}
	var storageAvailable atomic.Bool
	drain.stateWriter = func(path string, data []byte) error {
		if bytes.Contains(data, []byte(`"state":"idle"`)) && !storageAvailable.Load() {
			return errors.New("drain storage unavailable")
		}
		return atomicWriteState(path, data)
	}
	controller := NewHostedController(api, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, filepath.Join(root, "hosted.json"))
	controller.renewInterval = time.Millisecond
	writeAttempted := make(chan struct{})
	var writeAttemptedOnce sync.Once
	controller.stateWriter = func(path string, data []byte) error {
		writeAttemptedOnce.Do(func() { close(writeAttempted) })
		if !storageAvailable.Load() {
			return errors.New("Hosted storage unavailable")
		}
		return atomicWriteState(path, data)
	}
	done := make(chan error, 1)
	go func() { done <- controller.poll(context.Background()) }()
	select {
	case <-writeAttempted:
	case <-time.After(3 * time.Second):
		t.Fatal("initial assignment did not attempt to persist before cleanup")
	}
	select {
	case <-drain.Reopened():
		t.Fatal("admission reopened before cleanup became durable")
	case <-time.After(50 * time.Millisecond):
	}
	storageAvailable.Store(true)
	select {
	case <-drain.Reopened():
	case <-time.After(3 * time.Second):
		t.Fatal("initial assignment failure did not autonomously reopen admission")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("initial assignment persistence failure unexpectedly succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Hosted cleanup did not finish after storage recovered")
	}
	if directiveGets.Load() != 1 || drain.Status().State != DrainStateIdle || controller.Lifecycle().Active {
		t.Fatalf("gets=%d lifecycle=%#v drain=%#v", directiveGets.Load(), controller.Lifecycle(), drain.Status())
	}
}

func TestHostedReadyPersistenceFailureRemainsSupervisedWithoutDirectivePolling(t *testing.T) {
	var directiveGets atomic.Int32
	var leaseCalls atomic.Int32
	leaseExpiresAt := time.Now().Add(150 * time.Millisecond).UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet:
			directiveGets.Add(1)
			_, _ = w.Write([]byte(`{"schema_version":1,"directive":"update","update_id":"assigned","desired_version":"0.6.0","policy":"when_idle","drain_lease_seconds":60}`))
		case strings.HasSuffix(r.URL.Path, "/ready"):
			w.WriteHeader(http.StatusAccepted)
			_, _ = fmt.Fprintf(w, `{"schema_version":1,"accepted":true,"lease_expires_at":%q}`, leaseExpiresAt.Format(time.RFC3339Nano))
		case strings.HasSuffix(r.URL.Path, "/lease"):
			if leaseCalls.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"schema_version":1,"state":"cancelled","lease_expires_at":"0001-01-01T00:00:00Z"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
	root := t.TempDir()
	drain := NewDrainManager(nil, nil, 0, time.Now)
	if err := drain.SetPersistence(filepath.Join(root, "drain.json")); err != nil {
		t.Fatal(err)
	}
	var storageAvailable atomic.Bool
	drain.stateWriter = func(path string, data []byte) error {
		if bytes.Contains(data, []byte(`"state":"idle"`)) && !storageAvailable.Load() {
			return errors.New("drain storage unavailable")
		}
		return atomicWriteState(path, data)
	}
	controller := NewHostedController(api, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, filepath.Join(root, "hosted.json"))
	controller.progressInterval = time.Millisecond
	controller.renewInterval = time.Millisecond
	var writes atomic.Int32
	controller.stateWriter = func(path string, data []byte) error {
		if writes.Add(1) >= 3 && !storageAvailable.Load() {
			return errors.New("Hosted ready storage unavailable")
		}
		return atomicWriteState(path, data)
	}
	done := make(chan error, 1)
	go func() { done <- controller.poll(context.Background()) }()
	select {
	case <-drain.Reopened():
		t.Fatal("admission reopened before accepted readiness lease expiry")
	case <-time.After(75 * time.Millisecond):
	}
	storageAvailable.Store(true)
	select {
	case <-drain.Reopened():
	case <-time.After(3 * time.Second):
		t.Fatalf("ready persistence failure did not autonomously reopen admission: lifecycle=%#v drain=%#v writes=%d gets=%d", controller.Lifecycle(), drain.Status(), writes.Load(), directiveGets.Load())
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Hosted readiness supervisor did not finish")
	}
	if directiveGets.Load() != 1 || drain.Status().State != DrainStateIdle || controller.Lifecycle().Active {
		t.Fatalf("gets=%d lifecycle=%#v drain=%#v", directiveGets.Load(), controller.Lifecycle(), drain.Status())
	}
}

func TestDockerAgentReplaysDurableCreateIdempotencyAfterAcceptedResponseCrash(t *testing.T) {
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/update-requests":
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"schema_version":1,"request_id":"request-1","status":"accepted"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/update-requests/request-1":
			_, _ = w.Write([]byte(`{"schema_version":1,"request_id":"request-1","status":"succeeded","current_version":"0.6.0","target_version":"0.6.0","failure_code":""}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
	now := time.Now().UTC()
	root := t.TempDir()
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if _, err := drain.BeginDrain(DrainRequest{Lease: time.Hour}); err != nil {
		t.Fatal(err)
	}
	_ = drain.Status()
	_ = drain.Status()
	statePath := filepath.Join(root, "docker.json")
	writes := 0
	installer := &DockerAgentInstaller{API: api, Client: client, Current: CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, Drain: drain, StatePath: statePath, PollInterval: time.Millisecond}
	installer.stateWriter = func(path string, data []byte) error {
		writes++
		if writes == 2 {
			return errors.New("crash after accepted response")
		}
		return atomicWriteState(path, data)
	}
	release := VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}, Target: Target{Kind: "oci", ImageRef: "ghcr.io/openvibely/openvibely@sha256:" + strings.Repeat("a", 64)}}
	if err := installer.Apply(context.Background(), release); !errors.Is(err, ErrUpdateRecoveryPending) {
		t.Fatalf("accepted request save error=%v", err)
	}
	restored := &DockerAgentInstaller{API: api, StatePath: statePath, PollInterval: time.Millisecond}
	if err := restored.Load(); err != nil {
		t.Fatal(err)
	}
	if restored.state.Status != "creating" || restored.state.RequestID != "" || restored.state.CreateIdempotencyKey == "" {
		t.Fatalf("durable pre-acceptance state=%#v", restored.state)
	}
	if err := restored.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("idempotency keys=%q", keys)
	}
}

func TestDockerAgentAmbiguousCreateAndStatusFailuresRemainRecoveryPending(t *testing.T) {
	t.Run("accepted create response lost", func(t *testing.T) {
		var createKeys []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/v1/update-requests" {
				createKeys = append(createKeys, r.Header.Get("Idempotency-Key"))
				if len(createKeys) == 1 {
					hijacker, ok := w.(http.Hijacker)
					if !ok {
						t.Fatal("response writer cannot simulate response loss")
					}
					conn, _, err := hijacker.Hijack()
					if err != nil {
						t.Fatal(err)
					}
					_ = conn.Close()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"schema_version":1,"request_id":"request-1","status":"accepted"}`))
				return
			}
			if r.Method == http.MethodGet && r.URL.Path == "/v1/update-requests/request-1" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"schema_version":1,"request_id":"request-1","status":"succeeded","current_version":"0.6.0","target_version":"0.6.0"}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()
		api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
		root := t.TempDir()
		now := time.Now().UTC()
		client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
		drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
		status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		_ = drain.Status()
		installer := &DockerAgentInstaller{API: api, Client: client, Current: CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, Drain: drain, StatePath: filepath.Join(root, "docker.json"), PollInterval: time.Millisecond}
		release := VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}, Target: Target{Kind: "oci", ImageRef: "ghcr.io/openvibely/openvibely@sha256:" + strings.Repeat("a", 64)}}
		if err := installer.Apply(context.Background(), release); !errors.Is(err, ErrUpdateRecoveryPending) {
			t.Fatalf("ambiguous create error=%v", err)
		}
		if installer.state.Status != "creating" || installer.state.RequestID != "" || installer.state.CreateIdempotencyKey == "" {
			t.Fatalf("durable create state=%#v", installer.state)
		}
		if err := installer.Resume(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(createKeys) != 2 || createKeys[0] == "" || createKeys[0] != createKeys[1] {
			t.Fatalf("create keys=%q", createKeys)
		}
		if status.Generation != installer.state.DrainGeneration {
			t.Fatalf("drain generation=%q state=%#v", status.Generation, installer.state)
		}
	})

	t.Run("status unavailable", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()
		api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
		installer := &DockerAgentInstaller{API: api, StatePath: filepath.Join(t.TempDir(), "docker.json"), PollInterval: time.Millisecond, state: dockerPersistentState{RequestID: "request-1", Status: "replacing", CurrentVersion: "0.5.0", TargetVersion: "0.6.0", DrainGeneration: "generation"}}
		if err := installer.Resume(context.Background()); !errors.Is(err, ErrUpdateRecoveryPending) {
			t.Fatalf("ambiguous status error=%v", err)
		}
		if installer.state.Status != "replacing" || installer.state.RequestID != "request-1" {
			t.Fatalf("status failure changed durable request=%#v", installer.state)
		}
	})
}

func TestHostedLostReadinessResponseReplaysClaimAndUsesAuthoritativeLease(t *testing.T) {
	var readyCalls atomic.Int32
	var readyKeys []string
	authoritativeLease := time.Now().Add(350 * time.Millisecond).UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/ready"):
			readyKeys = append(readyKeys, r.Header.Get("Idempotency-Key"))
			call := readyCalls.Add(1)
			if call == 1 {
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					t.Fatal("response writer cannot simulate response loss")
				}
				conn, _, err := hijacker.Hijack()
				if err != nil {
					t.Fatal(err)
				}
				_ = conn.Close()
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			if call == 2 {
				_, _ = w.Write([]byte(`{"schema_version":`))
				return
			}
			_, _ = fmt.Fprintf(w, `{"schema_version":1,"accepted":true,"lease_expires_at":%q}`, authoritativeLease.Format(time.RFC3339Nano))
		case strings.HasSuffix(r.URL.Path, "/lease"):
			if time.Now().Before(authoritativeLease) {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"schema_version":1,"state":"cancelled","lease_expires_at":"0001-01-01T00:00:00Z"}`))
		default:
			t.Errorf("unexpected control-plane request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
	root := t.TempDir()
	drain := NewDrainManager(nil, nil, 0, time.Now)
	if err := drain.SetPersistence(filepath.Join(root, "drain.json")); err != nil {
		t.Fatal(err)
	}
	// The pre-claim lease only bounds coordinator startup; once the readiness
	// claim begins, ownership keeps the drain closed while responses are lost.
	// Leave enough startup headroom for heavily loaded CI runners.
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	_ = drain.Status()
	state := hostedPersistentState{UpdateID: "assigned", DesiredVersion: "0.6.0", Policy: "when_idle", DrainLeaseSeconds: 1, DrainGeneration: status.Generation, LeaseExpiresAt: status.ExpiresAt, Phase: StateWaitingForIdle}
	controller := NewHostedController(api, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, filepath.Join(root, "hosted.json"))
	controller.state = state
	controller.renewInterval = 5 * time.Millisecond
	done := make(chan error, 1)
	go func() {
		done <- controller.coordinate(context.Background(), HostedDirective{UpdateID: "assigned"}, state)
	}()
	deadline := time.Now().Add(time.Second)
	for readyCalls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if readyCalls.Load() < 3 || !drain.Owns(status.Generation) {
		t.Fatalf("readyCalls=%d owns=%v", readyCalls.Load(), drain.Owns(status.Generation))
	}
	select {
	case <-drain.Reopened():
		t.Fatal("admission reopened at the older pre-claim deadline")
	case <-time.After(150 * time.Millisecond):
	}
	select {
	case <-drain.Reopened():
	case <-time.After(3 * time.Second):
		t.Fatal("ambiguous readiness claim did not release at the authoritative deadline")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("readiness reconciliation error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("readiness supervisor did not finish")
	}
	if len(readyKeys) != 3 || readyKeys[0] == "" || readyKeys[0] != readyKeys[1] || readyKeys[0] != readyKeys[2] {
		t.Fatalf("readiness idempotency keys=%q", readyKeys)
	}
	if controller.Lifecycle().Active || drain.Status().State != DrainStateIdle {
		t.Fatalf("lifecycle=%#v drain=%#v", controller.Lifecycle(), drain.Status())
	}
}

func TestHostedLostReadinessResponseRemainsClosedWhileReplayUnavailable(t *testing.T) {
	var readyCalls atomic.Int32
	var firstKey atomic.Value
	readyStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/ready") {
			t.Errorf("unexpected control-plane request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if readyCalls.Add(1) == 1 {
			firstKey.Store(key)
			close(readyStarted)
		} else if got, _ := firstKey.Load().(string); key == "" || key != got {
			t.Errorf("readiness idempotency key changed from %q to %q", got, key)
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("response writer cannot simulate response loss")
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
	}))
	defer server.Close()
	api, _ := NewAgentHTTPClient(server.URL, "secret", server.Client())
	root := t.TempDir()
	drain := NewDrainManager(nil, nil, 0, time.Now)
	if err := drain.SetPersistence(filepath.Join(root, "drain.json")); err != nil {
		t.Fatal(err)
	}
	// The pre-claim lease only bounds coordinator startup; once the readiness
	// claim begins, ownership keeps the drain closed while responses are lost.
	// Leave enough startup headroom for heavily loaded CI runners.
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	_ = drain.Status()
	state := hostedPersistentState{UpdateID: "assigned", DesiredVersion: "0.6.0", Policy: "when_idle", DrainLeaseSeconds: 1, DrainGeneration: status.Generation, LeaseExpiresAt: status.ExpiresAt, Phase: StateWaitingForIdle}
	controller := NewHostedController(api, drain, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, filepath.Join(root, "hosted.json"))
	controller.state = state
	controller.renewInterval = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- controller.coordinate(ctx, HostedDirective{UpdateID: "assigned"}, state)
	}()
	select {
	case <-readyStarted:
	case <-time.After(time.Second):
		t.Fatal("readiness claim did not start")
	}
	select {
	case <-drain.Reopened():
		t.Fatal("admission reopened while readiness acceptance remained ambiguous")
	case <-time.After(120 * time.Millisecond):
	}
	if readyCalls.Load() < 2 || !drain.Owns(status.Generation) {
		t.Fatalf("readyCalls=%d owns=%v", readyCalls.Load(), drain.Owns(status.Generation))
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("coordinate error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("readiness replay did not stop with its context")
	}
	if !drain.Owns(status.Generation) || drain.Admit() {
		t.Fatal("context shutdown reopened an ambiguously accepted readiness claim")
	}
}

func TestAgentHTTPResponseBodyInterruptionIsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("response writer cannot simulate an interrupted body")
		}
		conn, buf, err := hijacker.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprint(buf, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"supported\":")
		_ = buf.Flush()
		_ = conn.Close()
	}))
	defer server.Close()
	api, err := NewAgentHTTPClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	err = api.Do(context.Background(), http.MethodGet, "/v1/capabilities", "", nil, &DockerCapabilities{}, http.StatusOK)
	if !errors.Is(err, ErrUpdateRetryable) {
		t.Fatalf("interrupted agent response error = %v, want retryable", err)
	}
}

func TestAgentHTTPRejectsRedirectWithoutForwardingToken(t *testing.T) {
	received := ""
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received = r.Header.Get("Authorization") }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer source.Close()
	api, _ := NewAgentHTTPClient(source.URL, "secret", source.Client())
	err := api.Do(context.Background(), http.MethodGet, "/v1/capabilities", "", nil, &DockerCapabilities{}, http.StatusOK)
	if err == nil {
		t.Fatal("redirect accepted")
	}
	if received != "" {
		t.Fatal("token forwarded to redirect target")
	}
}
