package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"github.com/openvibely/openvibely/internal/buildinfo"
	wailsupdater "github.com/wailsapp/wails/v3/pkg/updater"
)

const updateE2EEnv = "OPENVIBELY_UPDATE_E2E"

func TestPackagedUpdateE2E(t *testing.T) {
	if os.Getenv(updateE2EEnv) != "1" {
		t.Skip(updateE2EEnv + "=1 is required for packaged update E2E tests")
	}
	switch strings.TrimSpace(os.Getenv("OPENVIBELY_UPDATE_E2E_DISTRIBUTION")) {
	case "", "binary":
		t.Run("binary real app succeeds", testPackagedUpdateE2EBinarySucceeds)
		t.Run("binary real app rolls back", testPackagedUpdateE2EBinaryRollsBack)
	case "desktop":
		t.Run("desktop helper succeeds", testPackagedUpdateE2EDesktopHelperSucceeds)
		t.Run("desktop helper rolls back", testPackagedUpdateE2EDesktopHelperRollsBack)
	default:
		t.Fatalf("unknown OPENVIBELY_UPDATE_E2E_DISTRIBUTION %q", os.Getenv("OPENVIBELY_UPDATE_E2E_DISTRIBUTION"))
	}
}

func testPackagedUpdateE2EBinarySucceeds(t *testing.T) {
	runBinaryUpdateE2E(t, "0.6.0", "0.6.0", StateSucceeded)
}

func testPackagedUpdateE2EBinaryRollsBack(t *testing.T) {
	runBinaryUpdateE2E(t, "0.6.0", "0.7.0", StateRolledBack)
}

func runBinaryUpdateE2E(t *testing.T, releaseVersion, replacementVersion, wantState string) {
	t.Helper()
	root := t.TempDir()
	current := filepath.Join(root, executableName("openvibely"))
	replacement := filepath.Join(root, "replacement", executableName("openvibely"))
	buildGoCommand(t, "./cmd/server", current, map[string]string{
		"github.com/openvibely/openvibely/internal/buildinfo.Version":  "0.5.0",
		"github.com/openvibely/openvibely/internal/buildinfo.Commit":   "e2e-current",
		"github.com/openvibely/openvibely/internal/buildinfo.Artifact": "binary",
	})
	buildGoCommand(t, "./cmd/server", replacement, map[string]string{
		"github.com/openvibely/openvibely/internal/buildinfo.Version":  replacementVersion,
		"github.com/openvibely/openvibely/internal/buildinfo.Commit":   "e2e-replacement",
		"github.com/openvibely/openvibely/internal/buildinfo.Artifact": "binary",
	})
	archive, filetype, filename := packageBinaryArtifact(t, replacement)
	publicKeyFile, privateKey := writeE2ETrustRoot(t, root)
	updateServer := serveSignedBinaryRelease(t, archive, filetype, filename, releaseVersion, privateKey)
	defer updateServer.Close()

	port := freeTCPPort(t)
	appData := filepath.Join(root, "app-data")
	stdoutLog, stderrLog, readLogs := openCommandLogs(t, root, "binary-current")
	cmd := exec.Command(current)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"PORT="+port,
		"DATABASE_PATH="+filepath.Join(appData, "openvibely.db"),
		"PROJECT_REPO_ROOT="+filepath.Join(root, "projects"),
		"OPENVIBELY_APP_DATA_DIR="+appData,
		"OPENVIBELY_PLUGIN_ROOT="+filepath.Join(appData, "plugins"),
		"OPENVIBELY_UPDATE_SERVICE_URL="+updateServer.URL,
		"OPENVIBELY_UPDATE_PUBLIC_KEY_FILE="+publicKeyFile,
		"DISABLE_UPDATE_NOTIFICATIONS=false",
		"OPENVIBELY_DISABLE_INSTALL_ID=1",
		"OPENVIBELY_UPDATE_INTEGRATION_WAIT_TIMEOUT_MS=5000",
		"OPENVIBELY_UPDATE_INTEGRATION_VALIDATION_TIMEOUT_MS=5000",
	)
	cmd.Stdout = stdoutLog
	cmd.Stderr = stderrLog
	if err := cmd.Start(); err != nil {
		t.Fatalf("start current app: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		killPort(t, port)
	})

	baseURL := "http://127.0.0.1:" + port
	waitForHealthVersion(t, baseURL, "0.5.0")
	waitForUpdateState(t, baseURL, StateAvailable)
	resp, err := http.Post(baseURL+"/api/system/update/apply", "application/json", nil)
	if err != nil {
		t.Fatalf("accept update: %v\n%s", err, readLogs())
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("accept update HTTP %d\n%s", resp.StatusCode, readLogs())
	}
	if err := waitForCommandExit(cmd, time.Minute); err != nil {
		t.Fatalf("current app exit after handoff: %v\n%s", err, readLogs())
	}
	if wantState == StateSucceeded {
		waitForHealthVersion(t, baseURL, releaseVersion)
	} else {
		waitForHealthVersion(t, baseURL, "0.5.0")
	}
	waitForUpdateState(t, baseURL, wantState)
}

func testPackagedUpdateE2EDesktopHelperSucceeds(t *testing.T) {
	runDesktopHelperE2E(t, "0.6.0", "0.6.0", binaryOutcomeSucceeded)
}

func testPackagedUpdateE2EDesktopHelperRollsBack(t *testing.T) {
	runDesktopHelperE2E(t, "0.6.0", "0.7.0", binaryOutcomeRolledBack)
}

func runDesktopHelperE2E(t *testing.T, expectedVersion, replacementVersion, wantOutcome string) {
	t.Helper()
	root := t.TempDir()
	currentExe := filepath.Join(root, "current", executableName("openvibely-e2e-fixture"))
	replacementExe := filepath.Join(root, "replacement", executableName("openvibely-e2e-fixture"))
	buildGoCommand(t, "./internal/update/testfixture", currentExe, map[string]string{"main.version": "0.5.0"})
	buildGoCommand(t, "./internal/update/testfixture", replacementExe, map[string]string{"main.version": replacementVersion})

	installPath, installedExecutable, executableRelative := installDesktopUnit(t, root, "OpenVibely.app", currentExe)
	artifact, filetype, filename := packageDesktopArtifact(t, replacementExe)
	publicKeyFile, privateKey := writeE2ETrustRoot(t, root)
	updateServer := serveSignedRelease(t, artifact, filetype, filename, "app_bundle", expectedVersion, privateKey)
	defer updateServer.Close()
	updateKeys, err := DecodePublicKeys("", "", publicKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(ClientConfig{
		ServiceURL: updateServer.URL,
		Channel:    "stable",
		StatePath:  filepath.Join(root, "desktop-update-state.json"),
		PublicKeys: updateKeys,
	})
	current := CurrentBuild{
		Build:        buildinfo.Build{Version: "0.5.0", OS: runtime.GOOS, Arch: runtime.GOARCH},
		Distribution: buildinfo.DistributionDesktop,
	}
	release, checked, err := client.CheckIfDue(context.Background(), current)
	if err != nil {
		t.Fatalf("desktop update check: %v", err)
	}
	if !checked || release == nil {
		t.Fatal("desktop update check did not return a release")
	}
	dataRoot := filepath.Join(root, "data")
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	installer := &WailsInstaller{
		Updater:            wailsupdater.New(noopUpdaterHost{}),
		Provider:           &WailsProvider{Client: client, Current: current},
		AppPath:            installPath,
		ProtectedDataPaths: []string{dataRoot},
	}
	stagedValue, err := installer.Stage(context.Background(), *release)
	if err != nil {
		t.Fatalf("desktop stage: %v", err)
	}
	staged, ok := stagedValue.(LocalStagedUpdate)
	if !ok {
		t.Fatalf("desktop staged value = %T", stagedValue)
	}
	if err := retainDesktopInstallUnit(staged, []string{dataRoot}); err != nil {
		t.Fatalf("retain desktop install unit: %v", err)
	}
	if err := writeBinaryHelperOutcome(staged, binaryOutcomeAuthorized); err != nil {
		t.Fatalf("write helper authorization: %v", err)
	}
	helperPath := packagedUpdateHelperPath(staged.InstallPath)
	if err := copyFile(installedExecutable, helperPath, 0o755); err != nil {
		t.Fatalf("publish desktop helper fixture: %v", err)
	}

	port := freeTCPPort(t)
	t.Cleanup(func() { killPort(t, port) })
	parentPID := exitedFixtureParentPID(t, installedExecutable)
	helperArgs := []string{
		"desktop-update-helper",
		"--parent-pid", strconv.Itoa(parentPID),
		"--current", staged.InstallPath,
		"--staged", staged.ArtifactPath,
		"--backup", staged.BackupPath,
		"--health-url", "http://127.0.0.1:" + port + "/health",
		"--expected-version", staged.Version,
		"--previous-version", staged.PreviousVersion,
		"--outcome-id", staged.OutcomeID,
	}
	metadata, err := json.Marshal(binaryRelaunchMetadata{
		Arguments:          []string{installedExecutable, "serve", "--listen", "127.0.0.1:" + port},
		WorkingDirectory:   root,
		ExecutableRelative: executableRelative,
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(helperPath, helperArgs...)
	cmd.Stdin = bytes.NewReader(metadata)
	cmd.Env = append(os.Environ(),
		"OPENVIBELY_UPDATE_INTEGRATION_WAIT_TIMEOUT_MS=2000",
		"OPENVIBELY_UPDATE_INTEGRATION_VALIDATION_TIMEOUT_MS=5000",
	)
	if wantOutcome == binaryOutcomeSucceeded {
		cmd.Env = append(cmd.Env, "OPENVIBELY_UPDATE_INTEGRATION_EXIT_AFTER_HEALTH=1")
	}
	output, helperErr := cmd.CombinedOutput()
	if helperErr != nil && wantOutcome != binaryOutcomeRolledBack {
		t.Fatalf("desktop helper failed: %v\n%s", helperErr, output)
	}
	outcome, err := readBinaryHelperOutcome(staged)
	if err != nil {
		t.Fatalf("read desktop helper outcome: %v\nhelper err: %v\n%s", err, helperErr, output)
	}
	if outcome.State != wantOutcome {
		t.Fatalf("desktop outcome = %q, want %q\nhelper err: %v\n%s", outcome.State, wantOutcome, helperErr, output)
	}
	if wantOutcome == binaryOutcomeSucceeded {
		assertInstalledFixtureVersion(t, installedExecutable, replacementVersion)
	} else {
		assertInstalledFixtureVersion(t, installedExecutable, "0.5.0")
	}
}

func exitedFixtureParentPID(t *testing.T, executable string) int {
	t.Helper()
	port := freeTCPPort(t)
	cmd := exec.Command(executable, "serve", "--listen", "127.0.0.1:"+port)
	cmd.Env = append(os.Environ(), "OPENVIBELY_UPDATE_INTEGRATION_EXIT_AFTER_HEALTH=1")
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start exited parent fixture: %v", err)
	}
	pid := cmd.Process.Pid
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://127.0.0.1:" + port + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				if waitErr := cmd.Wait(); waitErr != nil {
					t.Fatalf("wait exited parent fixture: %v\n%s", waitErr, stderr.String())
				}
				return pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	t.Fatalf("exited parent fixture did not start: %s", stderr.String())
	return 0
}

func openCommandLogs(t *testing.T, dir, name string) (*os.File, *os.File, func() string) {
	t.Helper()
	stdoutPath := filepath.Join(dir, name+".stdout.log")
	stderrPath := filepath.Join(dir, name+".stderr.log")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatalf("create stdout log: %v", err)
	}
	stderr, err := os.Create(stderrPath)
	if err != nil {
		_ = stdout.Close()
		t.Fatalf("create stderr log: %v", err)
	}
	t.Cleanup(func() {
		_ = stdout.Close()
		_ = stderr.Close()
	})
	read := func() string {
		_ = stdout.Sync()
		_ = stderr.Sync()
		stdoutData, _ := os.ReadFile(stdoutPath)
		stderrData, _ := os.ReadFile(stderrPath)
		return fmt.Sprintf("stdout:\n%s\nstderr:\n%s", stdoutData, stderrData)
	}
	return stdout, stderr, read
}

func waitForCommandExit(cmd *exec.Cmd, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return fmt.Errorf("timed out after %s waiting for process %d to exit", timeout, cmd.Process.Pid)
	}
}

func buildGoCommand(t *testing.T, pkg, output string, values map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		t.Fatal(err)
	}
	args := []string{"build", "-o", output}
	if len(values) > 0 {
		var ldflags []string
		for key, value := range values {
			ldflags = append(ldflags, "-X", key+"="+value)
		}
		args = append(args, "-ldflags", strings.Join(ldflags, " "))
	}
	args = append(args, pkg)
	cmd := exec.Command("go", args...)
	cmd.Dir = repoRoot(t)
	outputBytes, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s failed: %v\n%s", strings.Join(args, " "), err, outputBytes)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			t.Fatal("repo root not found")
		}
		wd = parent
	}
}

func executableName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func packageBinaryArtifact(t *testing.T, executable string) ([]byte, string, string) {
	t.Helper()
	payload, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	entry := executableName("openvibely")
	switch runtime.GOOS {
	case "linux":
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		if err := tw.WriteHeader(&tar.Header{Name: entry, Mode: 0o755, Size: int64(len(payload))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes(), "tar.gz", "openvibely.tar.gz"
	default:
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		header := &zip.FileHeader{Name: entry, Method: zip.Deflate}
		header.SetMode(0o755)
		w, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes(), "zip", "openvibely.zip"
	}
}

func packageDesktopArtifact(t *testing.T, executable string) ([]byte, string, string) {
	t.Helper()
	payload, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if runtime.GOOS == "darwin" {
		writeZipEntry(t, zw, "OpenVibely.app/Contents/Info.plist", 0o644, []byte(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>CFBundleExecutable</key><string>OpenVibely</string></dict></plist>`))
		writeZipEntry(t, zw, "OpenVibely.app/Contents/MacOS/OpenVibely", 0o755, payload)
	} else {
		writeZipEntry(t, zw, executableName("openvibely-desktop"), 0o755, payload)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), "zip", "openvibely-desktop.zip"
}

func writeZipEntry(t *testing.T, zw *zip.Writer, name string, mode os.FileMode, payload []byte) {
	t.Helper()
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(mode)
	w, err := zw.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
}

func writeE2ETrustRoot(t *testing.T, root string) (string, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "update-public-keys.json")
	data, err := json.Marshal(map[string]string{"e2e": base64.StdEncoding.EncodeToString(public)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, private
}

func serveSignedBinaryRelease(t *testing.T, artifact []byte, filetype, filename, version string, private ed25519.PrivateKey) *httptest.Server {
	return serveSignedRelease(t, artifact, filetype, filename, "binary", version, private)
}

func serveSignedRelease(t *testing.T, artifact []byte, filetype, filename, kind, version string, private ed25519.PrivateKey) *httptest.Server {
	t.Helper()
	digest := sha256.Sum256(artifact)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/updates/check":
			target := Target{
				ID:       "binary-" + runtime.GOOS + "-" + runtime.GOARCH,
				Kind:     kind,
				OS:       runtime.GOOS,
				Arch:     runtime.GOARCH,
				URL:      server.URL + "/artifact/" + filename,
				Filename: filename,
				Filetype: filetype,
				Size:     int64(len(artifact)),
				SHA256:   hex.EncodeToString(digest[:]),
			}
			metadata := ReleaseMetadata{
				SchemaVersion:   1,
				Version:         version,
				Commit:          "e2e-release",
				Channel:         "stable",
				PublishedAt:     time.Now().Add(-time.Minute).UTC(),
				ExpiresAt:       time.Now().Add(time.Hour).UTC(),
				ReleaseNotesURL: "https://openvibely.ai/releases/" + version,
				Targets:         []Target{target},
			}
			raw, err := json.Marshal(metadata)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			canonical, err := jsoncanonicalizer.Transform(raw)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			response := CheckResponse{
				SchemaVersion:    1,
				UpdateAvailable:  true,
				LatestVersion:    version,
				Channel:          "stable",
				ApplySupported:   true,
				Action:           "download",
				ReleaseNotesURL:  metadata.ReleaseNotesURL,
				SelectedTargetID: target.ID,
				Release: &SignedRelease{
					Signed: raw,
					Signatures: []Signature{{
						KeyID:     "e2e",
						Algorithm: "ed25519",
						Value:     base64.StdEncoding.EncodeToString(ed25519.Sign(private, canonical)),
					}},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
		case "/artifact/" + filename:
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(artifact)
		default:
			http.NotFound(w, r)
		}
	}))
	return server
}

type noopUpdaterHost struct{}

func (noopUpdaterHost) Emit(string, ...any) bool         { return true }
func (noopUpdaterHost) OnEvent(string, func(any)) func() { return func() {} }
func (noopUpdaterHost) OpenWindow(wailsupdater.WindowOptions) wailsupdater.WindowHandle {
	return noopUpdaterWindow{}
}
func (noopUpdaterHost) Quit() {}

type noopUpdaterWindow struct{}

func (noopUpdaterWindow) EmitEvent(string, ...any) bool { return true }
func (noopUpdaterWindow) Show()                         {}
func (noopUpdaterWindow) Close()                        {}

func freeTCPPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
}

func waitForHealthVersion(t *testing.T, baseURL, version string) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/api/system/health")
		if err == nil {
			var body struct {
				Version string `json:"version"`
			}
			if resp.Body != nil {
				_ = json.NewDecoder(resp.Body).Decode(&body)
				_ = resp.Body.Close()
			}
			if resp.StatusCode == http.StatusOK && body.Version == version {
				return
			}
			last = fmt.Sprintf("HTTP %d version %q", resp.StatusCode, body.Version)
		} else {
			last = err.Error()
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("health did not report version %s: last=%s", version, last)
}

func waitForUpdateState(t *testing.T, baseURL, state string) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/api/system/update")
		if err == nil {
			var body struct {
				State string `json:"state"`
				Error string `json:"error"`
			}
			if resp.Body != nil {
				_ = json.NewDecoder(resp.Body).Decode(&body)
				_ = resp.Body.Close()
			}
			if resp.StatusCode == http.StatusOK && body.State == state {
				return
			}
			last = fmt.Sprintf("HTTP %d state %q error %q", resp.StatusCode, body.State, body.Error)
		} else {
			last = err.Error()
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("update state did not become %s: last=%s", state, last)
}

func installDesktopUnit(t *testing.T, root, name, executable string) (string, string, string) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		installPath := filepath.Join(root, name)
		executableRelative := "Contents/MacOS/OpenVibely"
		installedExecutable := filepath.Join(installPath, filepath.FromSlash(executableRelative))
		if err := copyFile(executable, installedExecutable, 0o755); err != nil {
			t.Fatal(err)
		}
		infoPath := filepath.Join(installPath, "Contents", "Info.plist")
		if err := os.WriteFile(infoPath, []byte(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>CFBundleExecutable</key><string>OpenVibely</string></dict></plist>`), 0o644); err != nil {
			t.Fatal(err)
		}
		return installPath, installedExecutable, executableRelative
	}
	installPath := filepath.Join(root, executableName("openvibely-desktop"))
	if strings.Contains(name, ".openvibely-new") {
		installPath += ".openvibely-new"
	}
	if err := copyFile(executable, installPath, 0o755); err != nil {
		t.Fatal(err)
	}
	return installPath, installPath, ""
}

func copyFile(source, destination string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, data, mode)
}

func assertInstalledFixtureVersion(t *testing.T, executable, version string) {
	t.Helper()
	port := freeTCPPort(t)
	cmd := exec.Command(executable, "serve", "--listen", "127.0.0.1:"+port)
	cmd.Env = append(os.Environ(), "OPENVIBELY_UPDATE_INTEGRATION_EXIT_AFTER_HEALTH=1")
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start installed fixture: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://127.0.0.1:" + port + "/health")
		if err == nil {
			var body struct {
				Version string `json:"version"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && body.Version == version {
				_ = cmd.Wait()
				return
			}
			last = fmt.Sprintf("HTTP %d version %q", resp.StatusCode, body.Version)
		} else {
			last = err.Error()
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	t.Fatalf("installed fixture did not report version %s: last=%s stderr=%s", version, last, stderr.String())
}

func killPort(t *testing.T, port string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if runtime.GOOS == "windows" {
		out, err := exec.CommandContext(ctx, "netstat", "-ano", "-p", "tcp").Output()
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 5 && strings.HasSuffix(fields[1], ":"+port) && fields[3] == "LISTENING" {
				_ = exec.CommandContext(ctx, "taskkill", "/PID", fields[4], "/T", "/F").Run()
			}
		}
		return
	}
	out, err := exec.CommandContext(ctx, "lsof", "-ti", "tcp:"+port).Output()
	if err != nil {
		return
	}
	for _, pid := range strings.Fields(string(out)) {
		_ = exec.CommandContext(ctx, "kill", pid).Run()
	}
}
