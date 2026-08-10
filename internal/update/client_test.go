package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/buildinfo"
)

func TestCanonicalJSONUsesRFC8785NumberSerialization(t *testing.T) {
	input := json.RawMessage(`{"numbers":[333333333.33333329,1E30,4.50,2e-3,0.000000000000000000000000001]}`)
	got, err := canonicalJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27]}`
	if string(got) != want {
		t.Fatalf("canonical JSON = %s, want %s", got, want)
	}
}

func TestClientSourceCheckIsMetricOnlyAndPersistsSuccess(t *testing.T) {
	unsetEnvForTest(t, installIDOptOutEnvForTest)
	var request map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/updates/check" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(CheckResponse{SchemaVersion: 1, UpdateAvailable: true, LatestVersion: "9.9.9", Action: "manual", Message: "upgrade"})
	}))
	defer srv.Close()
	dir := t.TempDir()
	now := time.Unix(1000, 0)
	client := NewClient(ClientConfig{ServiceURL: srv.URL, Channel: "main", StatePath: filepath.Join(dir, "update-state.json"), HTTPClient: srv.Client(), Now: func() time.Time { return now }, Random: func(time.Duration) time.Duration { return 0 }})
	current := CurrentBuild{Build: buildinfo.Build{Version: "dev-abc", Commit: "abc", OS: "darwin", Arch: "arm64"}, Distribution: buildinfo.DistributionSource}
	result, checked, err := client.CheckIfDue(context.Background(), current)
	if err != nil || !checked {
		t.Fatalf("result=%#v checked=%v err=%v", result, checked, err)
	}
	if result != nil {
		t.Fatalf("source check exposed update state: %#v", result)
	}
	if len(request) != 8 || request["distribution"] != buildinfo.DistributionSource {
		t.Fatalf("request=%#v", request)
	}
	if _, err := os.Stat(filepath.Join(dir, "update-state.json")); err != nil {
		t.Fatal(err)
	}
	if _, checked, err := client.CheckIfDue(context.Background(), current); err != nil || checked {
		t.Fatalf("second checked=%v err=%v", checked, err)
	}
}

func TestClientRejectsUnsupportedSchema(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"schema_version":2}`)) }))
	defer srv.Close()
	client := NewClient(ClientConfig{ServiceURL: srv.URL, Channel: "stable", StatePath: filepath.Join(t.TempDir(), "state.json"), HTTPClient: srv.Client()})
	_, _, err := client.CheckIfDue(context.Background(), CurrentBuild{Build: buildinfo.Build{Version: "1.0.0", Commit: "a", OS: "linux", Arch: "amd64"}, Distribution: buildinfo.DistributionBinary})
	if err == nil {
		t.Fatal("unsupported schema accepted")
	}
}

func TestCheckIfDueRetriesAfterReleaseVerificationFailureWithoutSuccessThrottle(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	requests := 0
	metadata := ReleaseMetadata{
		SchemaVersion:         1,
		Version:               "0.6.0",
		Commit:                "def",
		Channel:               "stable",
		PublishedAt:           now.Add(-time.Hour),
		ExpiresAt:             now.Add(24 * time.Hour),
		ReleaseNotesURL:       "https://openvibely.ai/releases/0.6.0",
		MinimumUpdaterVersion: "0.1.0",
		Targets: []Target{{
			ID:       "binary-linux-amd64",
			Kind:     "executable",
			OS:       "linux",
			Arch:     "amd64",
			URL:      "https://downloads.openvibely.ai/openvibely",
			Filename: "openvibely",
			Filetype: "binary",
			Size:     3,
			SHA256:   hex.EncodeToString(make([]byte, 32)),
		}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		response := signedCheckResponse(t, private, metadata)
		if requests == 1 {
			response.Release.Signatures[0].Value = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Fatal(err)
		}
	}))
	defer srv.Close()
	statePath := filepath.Join(t.TempDir(), "state.json")
	client := NewClient(ClientConfig{
		ServiceURL: srv.URL,
		Channel:    "stable",
		StatePath:  statePath,
		HTTPClient: srv.Client(),
		PublicKeys: map[string]ed25519.PublicKey{"release": public},
		Now:        func() time.Time { return now },
		Random:     func(time.Duration) time.Duration { return 0 },
	})
	current := CurrentBuild{Build: buildinfo.Build{Version: "0.5.0", Commit: "abc", OS: "linux", Arch: "amd64"}, Distribution: buildinfo.DistributionBinary}

	if release, checked, err := client.CheckIfDue(context.Background(), current); err == nil || !checked || release != nil {
		t.Fatalf("first release=%#v checked=%v err=%v, want verification failure", release, checked, err)
	}
	state, err := client.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if !state.LastSuccessfulCheck.IsZero() {
		t.Fatalf("verification failure persisted successful check %v", state.LastSuccessfulCheck)
	}
	if state.Failures != 1 || !state.NextAttempt.Equal(now.Add(time.Minute)) {
		t.Fatalf("failure state failures=%d next_attempt=%v", state.Failures, state.NextAttempt)
	}
	if requests != 1 {
		t.Fatalf("requests=%d, want 1", requests)
	}

	now = state.NextAttempt.Add(-time.Nanosecond)
	if release, checked, err := client.CheckIfDue(context.Background(), current); err != nil || checked || release != nil {
		t.Fatalf("before retry release=%#v checked=%v err=%v, want retry gate", release, checked, err)
	}
	if requests != 1 {
		t.Fatalf("requests before retry=%d, want 1", requests)
	}

	now = state.NextAttempt
	release, checked, err := client.CheckIfDue(context.Background(), current)
	if err != nil || !checked || release == nil {
		t.Fatalf("retry release=%#v checked=%v err=%v, want verified release", release, checked, err)
	}
	if release.Metadata.Version != "0.6.0" {
		t.Fatalf("release version=%q", release.Metadata.Version)
	}
	if requests != 2 {
		t.Fatalf("requests after retry=%d, want 2", requests)
	}
	state, err = client.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if !state.LastSuccessfulCheck.Equal(now) || state.Failures != 0 || !state.NextAttempt.Equal(now.Add(24*time.Hour)) || state.Cached == nil {
		t.Fatalf("successful retry state=%#v", state)
	}

	if cached, checked, err := client.CheckIfDue(context.Background(), current); err != nil || checked || cached == nil || cached.Metadata.Version != "0.6.0" {
		t.Fatalf("cached release=%#v checked=%v err=%v, want success throttle", cached, checked, err)
	}
	if requests != 2 {
		t.Fatalf("requests after success throttle=%d, want 2", requests)
	}
}
