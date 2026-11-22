package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/buildinfo"
)

// Exercise the real downloader and extractor while an update awaits approval.
// Advance the clock between checks instead of sleeping for the minute ticker.
func TestCoordinatorDoesNotRedownloadStagedUpdateAwaitingApproval(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	artifact := tarGzipBinaryFixture(t, "openvibely", []byte("new binary"))
	digest := sha256.Sum256(artifact)
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/artifact.tar.gz" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		downloads.Add(1)
		_, _ = w.Write(artifact)
	}))
	defer server.Close()

	root := t.TempDir()
	executable := filepath.Join(root, "openvibely")
	if err := os.WriteFile(executable, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	current := CurrentBuild{
		Build:        buildinfo.Build{Version: "0.6.0", OS: "linux", Arch: "amd64"},
		Distribution: buildinfo.DistributionBinary,
	}
	client := NewClient(ClientConfig{
		ServiceURL: server.URL,
		Channel:    "stable",
		StatePath:  filepath.Join(root, "client.json"),
		Now:        func() time.Time { return now },
	})
	target := Target{
		ID: "linux-amd64", Kind: "executable", OS: "linux", Arch: "amd64",
		URL: server.URL + "/artifact.tar.gz", Filename: "openvibely.tar.gz", Filetype: "tar.gz",
		Size: int64(len(artifact)), SHA256: hex.EncodeToString(digest[:]),
	}
	release := &VerifiedRelease{
		Metadata: ReleaseMetadata{
			Version: "0.7.0", Channel: "stable", ExpiresAt: now.Add(time.Hour), Targets: []Target{target},
		},
		Target: target, ApplySupported: true, Action: "download",
	}
	// Begin after release discovery, with valid cached metadata. Artifact requests
	// must still go through HTTP; no fake installer or download cache is used.
	if err := client.saveState(persistedClientState{LastSuccessfulCheck: now, Cached: release}); err != nil {
		t.Fatal(err)
	}
	installer := &BinaryInstaller{Client: client, Current: current, Executable: executable}
	coordinator := NewCoordinator(client, current, "stable", NewDrainManager(nil, nil, 0, nil), installer, false, "", nil)

	for minute := 0; minute < 3; minute++ {
		// Deliberately do not accept the update: it must stay available for approval.
		if err := coordinator.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
		snapshot := coordinator.Snapshot()
		if snapshot.State != StateAvailable || !snapshot.Staged {
			t.Fatalf("minute %d: expected a staged update awaiting approval, got %#v", minute, snapshot)
		}
		staged, err := os.ReadFile(executable + ".openvibely-new")
		if err != nil || string(staged) != "new binary" {
			t.Fatalf("minute %d: invalid staged binary: %q, error: %v", minute, staged, err)
		}
		t.Logf("minute %d: artifact HTTP downloads=%d, state=%s, staged=%v", minute, downloads.Load(), snapshot.State, snapshot.Staged)
		now = now.Add(time.Minute)
	}

	installed, err := os.ReadFile(executable)
	if err != nil || string(installed) != "old binary" {
		t.Fatalf("unaccepted update changed installed binary: %q, error: %v", installed, err)
	}
	if got := downloads.Load(); got != 1 {
		t.Fatalf("already-staged update was downloaded again: got %d artifact HTTP downloads across 3 checks, want 1", got)
	}
}
