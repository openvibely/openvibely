package update

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/buildinfo"
)

const installIDOptOutEnvForTest = "OPENVIBELY_DISABLE_INSTALL_ID"

var installIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type capturedCheckRequest struct {
	InstallID string `json:"install_id"`
}

func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	previous, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, previous)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func newInstallIDTestServer(t *testing.T, requests *[]capturedCheckRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedCheckRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		*requests = append(*requests, request)
		_ = json.NewEncoder(w).Encode(CheckResponse{SchemaVersion: checkSchemaVersion})
	}))
}

func installIDTestBuild() CurrentBuild {
	return CurrentBuild{
		Build:        buildinfo.Build{Version: "dev", Commit: "abc", OS: "linux", Arch: "amd64"},
		Distribution: buildinfo.DistributionSource,
	}
}

func TestCheckIfDueGeneratesPersistsSendsAndReusesInstallID(t *testing.T) {
	unsetEnvForTest(t, installIDOptOutEnvForTest)

	var requests []capturedCheckRequest
	server := newInstallIDTestServer(t, &requests)
	defer server.Close()

	now := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	statePath := filepath.Join(t.TempDir(), "update-state.json")
	client := NewClient(ClientConfig{
		ServiceURL: server.URL,
		Channel:    "stable",
		StatePath:  statePath,
		HTTPClient: server.Client(),
		Now:        func() time.Time { return now },
		Random:     func(time.Duration) time.Duration { return 0 },
	})

	if _, checked, err := client.CheckIfDue(context.Background(), installIDTestBuild()); err != nil || !checked {
		t.Fatalf("first check: checked=%v err=%v", checked, err)
	}
	if len(requests) != 1 || !installIDPattern.MatchString(requests[0].InstallID) {
		t.Fatalf("first request install_id=%q", requests[0].InstallID)
	}
	firstID := requests[0].InstallID
	state, err := client.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.InstallID != firstID || !state.InstallIDIssuedAt.Equal(now) {
		t.Fatalf("persisted install ID state = %#v", state)
	}

	now = now.Add(25 * time.Hour)
	if _, checked, err := client.CheckIfDue(context.Background(), installIDTestBuild()); err != nil || !checked {
		t.Fatalf("second check: checked=%v err=%v", checked, err)
	}
	if len(requests) != 2 || requests[1].InstallID != firstID {
		t.Fatalf("second request install_id=%q, want %q", requests[1].InstallID, firstID)
	}
	state, err = client.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.InstallID != firstID || !state.InstallIDIssuedAt.Equal(time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("reused install ID state = %#v", state)
	}
}

func TestCheckIfDueRotatesOnlyExpiredInstallID(t *testing.T) {
	unsetEnvForTest(t, installIDOptOutEnvForTest)

	now := time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name       string
		issuedAt   time.Time
		wantRotate bool
	}{
		{name: "older than rotation window", issuedAt: now.Add(-90*time.Hour*24 - time.Second), wantRotate: true},
		{name: "within rotation window", issuedAt: now.Add(-89 * 24 * time.Hour), wantRotate: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests []capturedCheckRequest
			server := newInstallIDTestServer(t, &requests)
			defer server.Close()

			const oldID = "00112233445566778899aabbccddeeff"
			statePath := filepath.Join(t.TempDir(), "update-state.json")
			client := NewClient(ClientConfig{ServiceURL: server.URL, Channel: "stable", StatePath: statePath, HTTPClient: server.Client(), Now: func() time.Time { return now }})
			if err := client.saveState(persistedClientState{InstallID: oldID, InstallIDIssuedAt: test.issuedAt}); err != nil {
				t.Fatal(err)
			}

			if _, checked, err := client.CheckIfDue(context.Background(), installIDTestBuild()); err != nil || !checked {
				t.Fatalf("checked=%v err=%v", checked, err)
			}
			if len(requests) != 1 {
				t.Fatalf("requests=%d", len(requests))
			}
			rotated := requests[0].InstallID != oldID
			if rotated != test.wantRotate {
				t.Fatalf("install_id=%q, old=%q, rotated=%v", requests[0].InstallID, oldID, rotated)
			}
			if !installIDPattern.MatchString(requests[0].InstallID) {
				t.Fatalf("install_id=%q is not 32 lowercase hex characters", requests[0].InstallID)
			}
		})
	}
}

func TestCheckIfDueOptOutOmitsAndDoesNotStoreInstallID(t *testing.T) {
	t.Setenv(installIDOptOutEnvForTest, "")

	var rawRequest map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&rawRequest); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(CheckResponse{SchemaVersion: checkSchemaVersion})
	}))
	defer server.Close()

	statePath := filepath.Join(t.TempDir(), "update-state.json")
	client := NewClient(ClientConfig{ServiceURL: server.URL, Channel: "stable", StatePath: statePath, HTTPClient: server.Client()})
	if _, checked, err := client.CheckIfDue(context.Background(), installIDTestBuild()); err != nil || !checked {
		t.Fatalf("checked=%v err=%v", checked, err)
	}
	if _, exists := rawRequest["install_id"]; exists {
		t.Fatalf("opted-out request contains install_id: %s", rawRequest["install_id"])
	}
	stateJSON, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stateJSON, []byte("install_id")) {
		t.Fatalf("opted-out state stores install ID fields: %s", stateJSON)
	}
}

func TestFreshClientsGenerateDifferentInstallIDs(t *testing.T) {
	unsetEnvForTest(t, installIDOptOutEnvForTest)

	var requests []capturedCheckRequest
	server := newInstallIDTestServer(t, &requests)
	defer server.Close()

	for range 2 {
		client := NewClient(ClientConfig{ServiceURL: server.URL, Channel: "stable", StatePath: filepath.Join(t.TempDir(), "state.json"), HTTPClient: server.Client()})
		if _, _, err := client.CheckIfDue(context.Background(), installIDTestBuild()); err != nil {
			t.Fatal(err)
		}
	}
	if len(requests) != 2 || requests[0].InstallID == requests[1].InstallID {
		t.Fatalf("fresh install IDs = %#v", requests)
	}
}

func TestInstallIDStateRemainsReadableByPreviousStruct(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	client := NewClient(ClientConfig{StatePath: filepath.Join(t.TempDir(), "state.json")})
	if err := client.saveState(persistedClientState{
		LastSuccessfulCheck: now,
		InstallID:           "00112233445566778899aabbccddeeff",
		InstallIDIssuedAt:   now,
	}); err != nil {
		t.Fatal(err)
	}

	type previousPersistedClientState struct {
		LastSuccessfulCheck    time.Time        `json:"last_successful_check"`
		HighestAcceptedVersion string           `json:"highest_accepted_version,omitempty"`
		MetadataExpiresAt      time.Time        `json:"metadata_expires_at,omitempty"`
		Cached                 *VerifiedRelease `json:"cached,omitempty"`
		Failures               int              `json:"failures,omitempty"`
		NextAttempt            time.Time        `json:"next_attempt,omitempty"`
	}
	data, err := os.ReadFile(client.cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	var previous previousPersistedClientState
	if err := json.Unmarshal(data, &previous); err != nil {
		t.Fatalf("previous state struct cannot read new state JSON: %v", err)
	}
	if !previous.LastSuccessfulCheck.Equal(now) {
		t.Fatalf("previous state lost known fields: %#v", previous)
	}
}

func TestInstallIDNeverAppearsInLogOutput(t *testing.T) {
	unsetEnvForTest(t, installIDOptOutEnvForTest)

	var requests []capturedCheckRequest
	server := newInstallIDTestServer(t, &requests)
	defer server.Close()

	var logs bytes.Buffer
	originalWriter := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(originalWriter) })

	client := NewClient(ClientConfig{ServiceURL: server.URL, Channel: "stable", StatePath: filepath.Join(t.TempDir(), "state.json"), HTTPClient: server.Client()})
	if _, _, err := client.CheckIfDue(context.Background(), installIDTestBuild()); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].InstallID == "" {
		t.Fatalf("request=%#v", requests)
	}
	if strings.Contains(logs.String(), requests[0].InstallID) {
		t.Fatalf("install ID appeared in logs: %q", logs.String())
	}
}

func TestReadOnlyStateDirectoryDoesNotFailInstallIDCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits do not make directories read-only on Windows")
	}
	unsetEnvForTest(t, installIDOptOutEnvForTest)

	var requests []capturedCheckRequest
	server := newInstallIDTestServer(t, &requests)
	defer server.Close()

	stateDir := t.TempDir()
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o700) })

	client := NewClient(ClientConfig{ServiceURL: server.URL, Channel: "stable", StatePath: filepath.Join(stateDir, "state.json"), HTTPClient: server.Client()})
	if _, checked, err := client.CheckIfDue(context.Background(), installIDTestBuild()); err != nil || !checked {
		t.Fatalf("read-only state check: checked=%v err=%v", checked, err)
	}
	if len(requests) != 1 || !installIDPattern.MatchString(requests[0].InstallID) {
		t.Fatalf("request=%#v", requests)
	}
}
