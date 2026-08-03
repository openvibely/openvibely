package update

import (
	"context"
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
	if len(request) != 7 || request["distribution"] != buildinfo.DistributionSource {
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
