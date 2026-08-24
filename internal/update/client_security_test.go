package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/buildinfo"
)

func signedCheckResponse(t *testing.T, private ed25519.PrivateKey, metadata ReleaseMetadata) CheckResponse {
	t.Helper()
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return CheckResponse{SchemaVersion: 1, UpdateAvailable: true, LatestVersion: metadata.Version, Channel: metadata.Channel, ApplySupported: true, Action: "download", ReleaseNotesURL: metadata.ReleaseNotesURL, SelectedTargetID: metadata.Targets[0].ID, Release: &SignedRelease{Signed: raw, Signatures: []Signature{{KeyID: "release", Algorithm: "ed25519", Value: base64.StdEncoding.EncodeToString(ed25519.Sign(private, canonical))}}}}
}

func TestCheckIfDueFailsClosedWhenRollbackStateIsCorrupt(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	corrupt := []byte(`{"highest_accepted_version":`)
	if err := os.WriteFile(statePath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	client := NewClient(ClientConfig{
		Channel:    "stable",
		ServiceURL: server.URL,
		StatePath:  statePath,
		Now:        func() time.Time { return time.Unix(1000, 0).UTC() },
	})
	if _, _, err := client.CheckIfDue(context.Background(), CurrentBuild{}); err == nil {
		t.Fatal("update check accepted corrupt rollback-protection state")
	}
	if requests.Load() != 0 {
		t.Fatalf("update service requests = %d, want 0", requests.Load())
	}
	got, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(corrupt) {
		t.Fatalf("corrupt rollback state was overwritten: %q", got)
	}
}

func TestCheckIfDueFailsClosedWhenRollbackStateCannotBeRead(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := os.Mkdir(statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	client := NewClient(ClientConfig{Channel: "stable", ServiceURL: server.URL, StatePath: statePath})
	if _, _, err := client.CheckIfDue(context.Background(), CurrentBuild{}); err == nil {
		t.Fatal("update check accepted unreadable rollback-protection state")
	}
	if requests.Load() != 0 {
		t.Fatalf("update service requests = %d, want 0", requests.Load())
	}
}

func TestVerifyReleaseRejectsEveryUntrustedMetadataClass(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	valid := ReleaseMetadata{SchemaVersion: 1, Version: "0.6.0", Commit: "def", Channel: "stable", PublishedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), ReleaseNotesURL: "https://openvibely.ai/releases/0.6.0", MinimumUpdaterVersion: "0.1.0", Targets: []Target{{ID: "binary-darwin-arm64", Kind: "executable", OS: "darwin", Arch: "arm64", URL: "https://downloads.openvibely.ai/openvibely", Filename: "openvibely", Filetype: "binary", Size: 3, SHA256: hex.EncodeToString(make([]byte, 32))}}}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "state.json"), PublicKeys: map[string]ed25519.PublicKey{"release": public}, Now: func() time.Time { return now }})
	current := CurrentBuild{Build: buildinfo.Build{Version: "0.5.0", OS: "darwin", Arch: "arm64"}, Distribution: buildinfo.DistributionBinary}
	if _, err := client.verifyRelease(signedCheckResponse(t, private, valid), current, "", now); err != nil {
		t.Fatalf("valid release: %v", err)
	}
	cases := map[string]func(*ReleaseMetadata, *CheckResponse){
		"expired":         func(m *ReleaseMetadata, _ *CheckResponse) { m.ExpiresAt = now },
		"schema":          func(m *ReleaseMetadata, _ *CheckResponse) { m.SchemaVersion = 2 },
		"target kind":     func(m *ReleaseMetadata, _ *CheckResponse) { m.Targets[0].Kind = "app_bundle" },
		"target OS":       func(m *ReleaseMetadata, _ *CheckResponse) { m.Targets[0].OS = "linux" },
		"target purpose":  func(m *ReleaseMetadata, _ *CheckResponse) { m.Targets[0].Purpose = "bootstrap_install" },
		"target variant":  func(m *ReleaseMetadata, _ *CheckResponse) { m.Targets[0].Variant = "binary" },
		"target layout":   func(m *ReleaseMetadata, _ *CheckResponse) { m.Targets[0].InstallLayout = "executable" },
		"size":            func(m *ReleaseMetadata, _ *CheckResponse) { m.Targets[0].Size = 0 },
		"digest":          func(m *ReleaseMetadata, _ *CheckResponse) { m.Targets[0].SHA256 = "bad" },
		"artifact URL":    func(m *ReleaseMetadata, _ *CheckResponse) { m.Targets[0].URL = "http://example.com/openvibely" },
		"invalid version": func(m *ReleaseMetadata, _ *CheckResponse) { m.Version = "release-next" },
		"version hint":    func(_ *ReleaseMetadata, r *CheckResponse) { r.LatestVersion = "0.7.0" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			metadata := valid
			metadata.Targets = append([]Target(nil), valid.Targets...)
			response := signedCheckResponse(t, private, metadata)
			mutate(&metadata, &response)
			if name != "version hint" {
				response = signedCheckResponse(t, private, metadata)
			}
			if _, err := client.verifyRelease(response, current, "", now); err == nil {
				t.Fatal("untrusted release accepted")
			}
		})
	}
	t.Run("invalid signature", func(t *testing.T) {
		response := signedCheckResponse(t, private, valid)
		response.Release.Signatures[0].Value = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
		if _, err := client.verifyRelease(response, current, "", now); err == nil {
			t.Fatal("invalid signature accepted")
		}
	})
}

func TestVerifyReleaseAcceptsLinuxDesktopExecutableTarget(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	metadata := ReleaseMetadata{SchemaVersion: 1, Version: "0.6.0", Commit: "def", Channel: "stable", PublishedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), ReleaseNotesURL: "https://openvibely.ai/releases/0.6.0", MinimumUpdaterVersion: "0.1.0", Targets: []Target{{ID: "download-desktop-linux-amd64", Kind: "executable", OS: "linux", Arch: "amd64", URL: "https://downloads.openvibely.ai/openvibely-desktop.tar.gz", Filename: "openvibely-desktop.tar.gz", Filetype: "tar.gz", Size: 3, SHA256: hex.EncodeToString(make([]byte, 32))}}}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "state.json"), PublicKeys: map[string]ed25519.PublicKey{"release": public}, Now: func() time.Time { return now }})
	current := CurrentBuild{Build: buildinfo.Build{Version: "0.5.0", OS: "linux", Arch: "amd64"}, Distribution: buildinfo.DistributionDesktop}
	release, err := client.verifyRelease(signedCheckResponse(t, private, metadata), current, "", now)
	if err != nil {
		t.Fatalf("linux desktop executable release: %v", err)
	}
	if release.Target.Kind != "executable" {
		t.Fatalf("target kind = %q, want executable", release.Target.Kind)
	}
}

func TestVerifyReleaseDowngradesNewerUpdaterRequirementToManual(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	metadata := ReleaseMetadata{SchemaVersion: 1, Version: "0.6.0", Commit: "def", Channel: "stable", ExpiresAt: now.Add(time.Hour), MinimumUpdaterVersion: "9.0.0", Targets: []Target{{ID: "binary-darwin-arm64", Kind: "executable", OS: "darwin", Arch: "arm64", URL: "https://downloads.openvibely.ai/openvibely", Filename: "openvibely", Filetype: "binary", Size: 3, SHA256: hex.EncodeToString(make([]byte, 32))}}}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "state.json"), PublicKeys: map[string]ed25519.PublicKey{"release": public}, Now: func() time.Time { return now }})
	current := CurrentBuild{Build: buildinfo.Build{Version: "0.5.0", OS: "darwin", Arch: "arm64"}, Distribution: buildinfo.DistributionBinary}
	response := signedCheckResponse(t, private, metadata)
	response.ApplySupported = false
	response.Action = "manual"
	response.SelectedTargetID = ""
	release, err := client.verifyRelease(response, current, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if release.ApplySupported || release.Action != "manual" {
		t.Fatalf("release apply_supported=%v action=%q", release.ApplySupported, release.Action)
	}
	if err := client.ValidateForInstall(*release, current); err == nil {
		t.Fatal("manual-only release was accepted for installation")
	}
}

func TestVerifyReleaseRejectsMalformedOCIDigestReference(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	metadata := ReleaseMetadata{SchemaVersion: 1, Version: "0.6.0", Commit: "def", Channel: "stable", PublishedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), MinimumUpdaterVersion: "0.1.0", Targets: []Target{{ID: "docker-linux-multiarch", Kind: "oci", OS: "linux", Arch: "multi", ImageRef: "ghcr.io/openvibely/openvibely@sha256:not-a-digest"}}}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "state.json"), PublicKeys: map[string]ed25519.PublicKey{"release": public}, Now: func() time.Time { return now }})
	current := CurrentBuild{Build: buildinfo.Build{Version: "0.5.0", OS: "linux", Arch: "amd64"}, Distribution: buildinfo.DistributionDocker}
	response := signedCheckResponse(t, private, metadata)
	response.Action = "container"
	if _, err := client.verifyRelease(response, current, "", now); err == nil {
		t.Fatal("malformed OCI digest accepted")
	}
}

func TestValidOCIImageRefValidatesCompleteReference(t *testing.T) {
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, ref := range []string{
		"ghcr.io/openvibely/openvibely@sha256:" + digest,
		"localhost:5000/openvibely/server:v0.6.0@sha256:" + digest,
		"openvibely/openvibely@sha256:" + digest,
	} {
		if !validOCIImageRef(ref) {
			t.Errorf("valid OCI reference rejected: %q", ref)
		}
	}
	for _, ref := range []string{
		"not an image@sha256:" + digest,
		"https://ghcr.io/openvibely/openvibely@sha256:" + digest,
		"ghcr.io//openvibely@sha256:" + digest,
		"ghcr.io/OpenVibely/openvibely@sha256:" + digest,
		"ghcr.io/openvibely/openvibely@:sha256:" + digest,
		"ghcr.io/openvibely/openvibely@sha256:" + digest + "extra",
	} {
		if validOCIImageRef(ref) {
			t.Errorf("malformed OCI reference accepted: %q", ref)
		}
	}
}

func TestDecodePublicKeysRejectsConfiguredEmbeddedKeyID(t *testing.T) {
	embedded, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	replacement, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "keys.json")
	data, err := json.Marshal(map[string]string{
		"official": base64.StdEncoding.EncodeToString(replacement),
		"rotation": base64.StdEncoding.EncodeToString(replacement),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePublicKeys("official", base64.StdEncoding.EncodeToString(embedded), path); err == nil {
		t.Fatal("configured key replaced the embedded official trust root")
	}
}

func TestFetchRejectsWrongSizeAndDigest(t *testing.T) {
	for name, payload := range map[string][]byte{"wrong size": []byte("toolong"), "wrong digest": []byte("abc")} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) }))
			defer server.Close()
			now := time.Now()
			client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "state.json"), HTTPClient: server.Client(), Now: func() time.Time { return now }})
			digest := sha256.Sum256([]byte("different"))
			size := int64(3)
			if name == "wrong size" {
				digest = sha256.Sum256(payload)
			}
			release := VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}, Target: Target{Kind: "executable", URL: server.URL, Size: size, SHA256: hex.EncodeToString(digest[:])}}
			_, err := client.Fetch(context.Background(), release, filepath.Join(t.TempDir(), "artifact"))
			if err == nil {
				t.Fatal("invalid artifact accepted")
			}
			if errors.Is(err, ErrUpdateRetryable) {
				t.Fatalf("definitive artifact integrity failure marked retryable: %v", err)
			}
			var downloaded bytes.Buffer
			err = client.Download(context.Background(), release, &downloaded, nil)
			if err == nil {
				t.Fatal("invalid artifact accepted by download")
			}
			if errors.Is(err, ErrUpdateRetryable) {
				t.Fatalf("definitive download integrity failure marked retryable: %v", err)
			}
		})
	}
}

func TestArtifactCopyClassifiesSourceAndDestinationFailures(t *testing.T) {
	destinationErr := errors.New("destination storage full")
	for name, writer := range map[string]io.Writer{
		"write error": errorWriter{err: destinationErr},
		"short write": shortWriter{},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := copyArtifactPayload(writer, bytes.NewReader([]byte("artifact")), 8)
			if err == nil {
				t.Fatal("destination failure accepted")
			}
			if errors.Is(err, ErrUpdateRetryable) {
				t.Fatalf("definitive destination failure marked retryable: %v", err)
			}
		})
	}

	_, _, err := copyArtifactPayload(errorWriter{err: destinationErr}, bytes.NewReader([]byte("artifact")), 8)
	if !errors.Is(err, destinationErr) {
		t.Fatalf("destination error = %v, want %v", err, destinationErr)
	}

	sourceErr := errors.New("response body interrupted")
	_, _, err = copyArtifactPayload(&bytes.Buffer{}, errorReader{err: sourceErr}, 8)
	if !errors.Is(err, sourceErr) {
		t.Fatalf("source error = %v, want %v", err, sourceErr)
	}
	if !errors.Is(err, ErrUpdateRetryable) {
		t.Fatalf("source interruption not marked retryable: %v", err)
	}
}

func TestDownloadDestinationFailureIsDefinitive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("artifact"))
	}))
	defer server.Close()
	now := time.Now()
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "state.json"), HTTPClient: server.Client(), Now: func() time.Time { return now }})
	digest := sha256.Sum256([]byte("artifact"))
	release := VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}, Target: Target{Kind: "executable", URL: server.URL, Size: 8, SHA256: hex.EncodeToString(digest[:])}}
	destinationErr := errors.New("destination storage full")

	err := client.Download(context.Background(), release, errorWriter{err: destinationErr}, nil)
	if !errors.Is(err, destinationErr) {
		t.Fatalf("download error = %v, want %v", err, destinationErr)
	}
	if errors.Is(err, ErrUpdateRetryable) {
		t.Fatalf("definitive download destination failure marked retryable: %v", err)
	}
}

type errorWriter struct{ err error }

func (w errorWriter) Write([]byte) (int, error) { return 0, w.err }

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

func TestArtifactRedirectPolicyFailureIsDefinitive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path, http.StatusFound)
	}))
	defer server.Close()
	now := time.Now()
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "state.json"), HTTPClient: server.Client(), Now: func() time.Time { return now }})
	digest := sha256.Sum256([]byte("artifact"))
	release := VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}, Target: Target{Kind: "executable", URL: server.URL, Size: 8, SHA256: hex.EncodeToString(digest[:])}}

	if _, err := client.Fetch(context.Background(), release, filepath.Join(t.TempDir(), "artifact")); err == nil {
		t.Fatal("redirect loop accepted")
	} else if errors.Is(err, ErrUpdateRetryable) {
		t.Fatalf("redirect policy failure marked retryable: %v", err)
	}
	if err := client.Download(context.Background(), release, &bytes.Buffer{}, nil); err == nil {
		t.Fatal("redirect loop accepted by download")
	} else if errors.Is(err, ErrUpdateRetryable) {
		t.Fatalf("download redirect policy failure marked retryable: %v", err)
	}
}
