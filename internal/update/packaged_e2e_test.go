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
	"net/url"
	"os"
	"os/exec"
	"path"
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
	distribution := strings.TrimSpace(os.Getenv("OPENVIBELY_UPDATE_E2E_DISTRIBUTION"))
	assertPackagedUpdateMatrixTarget(t, distribution)
	switch distribution {
	case "", "binary":
		t.Run("binary real app succeeds", testPackagedUpdateE2EBinarySucceeds)
		t.Run("binary real app rolls back", testPackagedUpdateE2EBinaryRollsBack)
		t.Run("binary real recovery helper succeeds", testPackagedUpdateE2EBinaryRecoverySucceeds)
		t.Run("binary real recovery helper rolls back", testPackagedUpdateE2EBinaryRecoveryRollsBack)
	case "desktop":
		t.Run("app-bundle update helper succeeds", testPackagedUpdateE2EAppBundleUpdateHelperSucceeds)
		t.Run("app-bundle update helper rolls back", testPackagedUpdateE2EAppBundleUpdateHelperRollsBack)
		t.Run("desktop real app succeeds", testPackagedUpdateE2ERealDesktopSucceeds)
		t.Run("desktop real app rolls back", testPackagedUpdateE2ERealDesktopRollsBack)
		t.Run("desktop real recovery helper succeeds", testPackagedUpdateE2EDesktopRecoverySucceeds)
		t.Run("desktop real recovery helper rolls back", testPackagedUpdateE2EDesktopRecoveryRollsBack)
	default:
		t.Fatalf("unknown OPENVIBELY_UPDATE_E2E_DISTRIBUTION %q", os.Getenv("OPENVIBELY_UPDATE_E2E_DISTRIBUTION"))
	}
}

func assertPackagedUpdateMatrixTarget(t *testing.T, distribution string) {
	t.Helper()
	expectedOS := strings.TrimSpace(os.Getenv("OPENVIBELY_UPDATE_E2E_EXPECTED_OS"))
	expectedArch := strings.TrimSpace(os.Getenv("OPENVIBELY_UPDATE_E2E_EXPECTED_ARCH"))
	if expectedOS == "" && expectedArch == "" {
		return
	}
	normalizedOS := map[string]string{"Linux": "linux", "macOS": "darwin", "Windows": "windows"}[expectedOS]
	if normalizedOS == "" {
		t.Fatalf("unknown expected packaged-update OS %q", expectedOS)
	}
	if runtime.GOOS != normalizedOS {
		t.Fatalf("packaged-update OS mismatch: runtime.GOOS=%s expected=%s", runtime.GOOS, normalizedOS)
	}
	if expectedArch != "" && runtime.GOARCH != expectedArch {
		t.Fatalf("packaged-update arch mismatch: runtime.GOARCH=%s expected=%s", runtime.GOARCH, expectedArch)
	}
	if distribution != "binary" && distribution != "desktop" {
		t.Fatalf("packaged-update distribution must be binary or desktop when matrix expectations are set, got %q", distribution)
	}
	t.Logf("packaged update matrix target: os=%s arch=%s distribution=%s", runtime.GOOS, runtime.GOARCH, distribution)
}

func testPackagedUpdateE2EBinarySucceeds(t *testing.T) {
	t.Run("direct executable", func(t *testing.T) {
		runBinaryUpdateE2E(t, "0.6.0", "0.6.0", StateSucceeded, binaryE2EDirectLayout)
	})
	t.Run("command symlink with unwritable command dir", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows production binary installs do not use Unix command symlinks")
		}
		runBinaryUpdateE2E(t, "0.6.0", "0.6.0", StateSucceeded, binaryE2ESymlinkLayout)
	})
}

func testPackagedUpdateE2EBinaryRollsBack(t *testing.T) {
	runBinaryUpdateE2E(t, "0.6.0", "0.7.0", StateRolledBack, binaryE2EDirectLayout)
}

func testPackagedUpdateE2EBinaryRecoverySucceeds(t *testing.T) {
	runExecutableRecoveryProcessE2E(t, buildinfo.DistributionBinary, "0.6.0", packagedUpdateOutcomeSucceeded)
}

func testPackagedUpdateE2EBinaryRecoveryRollsBack(t *testing.T) {
	runExecutableRecoveryProcessE2E(t, buildinfo.DistributionBinary, "0.7.0", packagedUpdateOutcomeRolledBack)
}

type binaryE2EInstallLayout string

const (
	binaryE2EDirectLayout  binaryE2EInstallLayout = "direct"
	binaryE2ESymlinkLayout binaryE2EInstallLayout = "symlink"
)

func runBinaryUpdateE2E(t *testing.T, releaseVersion, replacementVersion, wantState string, layout binaryE2EInstallLayout) {
	t.Helper()
	root := t.TempDir()
	current := filepath.Join(root, executableName("openvibely"))
	launchPath := current
	commandDir := ""
	if layout == binaryE2ESymlinkLayout {
		current = filepath.Join(root, "appbin", executableName("openvibely"))
		commandDir = filepath.Join(root, "command")
		launchPath = filepath.Join(commandDir, executableName("openvibely"))
		if err := os.MkdirAll(commandDir, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(commandDir, 0o755) })
	}
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
	if layout == binaryE2ESymlinkLayout {
		if err := os.Symlink(current, launchPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(commandDir, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	archive, filetype, filename := packageBinaryArtifact(t, replacement)
	publicKeyFile, privateKey := writeE2ETrustRoot(t, root)
	updateServer := serveSignedBinaryRelease(t, archive, filetype, filename, releaseVersion, privateKey)
	defer updateServer.Close()

	port := freeTCPPort(t)
	appData := filepath.Join(root, "app-data")
	stdoutLog, stderrLog, readLogs := openCommandLogs(t, root, "binary-current")
	cmd := exec.Command(launchPath)
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
	waitForStagedUpdate(t, baseURL)
	resp, err := http.Post(baseURL+"/api/system/update/apply", "application/json", nil)
	if err != nil {
		t.Fatalf("accept update: %v\n%s", err, readLogs())
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("accept update HTTP %d\n%s", resp.StatusCode, readLogs())
	}
	if err := waitForCommandExit(cmd, time.Minute); err != nil {
		t.Fatalf("current app exit after handoff: %v\nupdate snapshot: %s\nhelper state:\n%s\n%s", err, readUpdateSnapshot(baseURL), describePackagedUpdateHelperState(current), readLogs())
	}
	if wantState == StateSucceeded {
		waitForHealthVersionWithin(t, baseURL, releaseVersion, 120*time.Second, func() string {
			return fmt.Sprintf("\nupdate snapshot: %s\nhelper state:\n%s\n%s", readUpdateSnapshot(baseURL), describePackagedUpdateHelperState(current), readLogs())
		})
	} else {
		waitForHealthVersionWithin(t, baseURL, "0.5.0", 120*time.Second, func() string {
			return fmt.Sprintf("\nupdate snapshot: %s\nhelper state:\n%s\n%s", readUpdateSnapshot(baseURL), describePackagedUpdateHelperState(current), readLogs())
		})
	}
	waitForUpdateState(t, baseURL, wantState)
	if layout == binaryE2ESymlinkLayout {
		assertBinarySymlinkLayoutUpdated(t, commandDir, launchPath, current, releaseVersion, wantState)
	}
}

func runExecutableRecoveryProcessE2E(t *testing.T, distribution, runningVersion, wantOutcome string) {
	t.Helper()
	root := t.TempDir()
	baseName := "openvibely"
	build := func(output, version string) {
		buildGoCommand(t, "./cmd/server", output, map[string]string{
			"github.com/openvibely/openvibely/internal/buildinfo.Version":  version,
			"github.com/openvibely/openvibely/internal/buildinfo.Commit":   "e2e-recovery-" + version,
			"github.com/openvibely/openvibely/internal/buildinfo.Artifact": "binary",
		})
	}
	if distribution == buildinfo.DistributionDesktop {
		baseName = "openvibely-desktop"
		build = func(output, version string) { buildDesktopCommand(t, output, version) }
	}

	predecessor := filepath.Join(root, "build", "predecessor", executableName(baseName))
	replacement := filepath.Join(root, "build", "replacement", executableName(baseName))
	current := filepath.Join(root, "install", executableName(baseName))
	build(predecessor, "0.5.0")
	build(replacement, runningVersion)
	if err := copyFile(replacement, current, 0o755); err != nil {
		t.Fatal(err)
	}
	staged := LocalStagedUpdate{
		ArtifactPath:    current + ".openvibely-new",
		InstallPath:     current,
		BackupPath:      current + ".openvibely-backup",
		Version:         "0.6.0",
		PreviousVersion: "0.5.0",
		OutcomeID:       "e2e-recovery-" + distribution + "-" + wantOutcome,
	}
	if err := copyFile(predecessor, staged.BackupPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writePackagedUpdateHelperPhase(staged, packagedUpdateOutcomeTargetPublished); err != nil {
		t.Fatal(err)
	}

	appData := filepath.Join(root, "app-data")
	preparePackagedRecoveryPersistence(t, appData, staged, distribution, runningVersion)
	processEnv := append(envWithout("PORT", "OPENVIBELY_UPDATE_E2E_HEADLESS_DESKTOP"),
		"DATABASE_PATH="+filepath.Join(appData, "openvibely.db"),
		"PROJECT_REPO_ROOT="+filepath.Join(root, "projects"),
		"OPENVIBELY_APP_DATA_DIR="+appData,
		"OPENVIBELY_PLUGIN_ROOT="+filepath.Join(appData, "plugins"),
		"DISABLE_UPDATE_NOTIFICATIONS=false",
		"OPENVIBELY_DISABLE_INSTALL_ID=1",
		"AUTH_ENABLED=false",
		"OPENVIBELY_UPDATE_INTEGRATION_WAIT_TIMEOUT_MS=10000",
		"OPENVIBELY_UPDATE_INTEGRATION_VALIDATION_TIMEOUT_MS=15000",
	)
	port := ""
	baseURL := ""
	parentStdout, parentStderr, readParentLogs := openCommandLogs(t, root, "recovery-parent")
	parent := exec.Command(current)
	parent.Dir = root
	parent.Env = processEnv
	if distribution != buildinfo.DistributionDesktop {
		port = freeTCPPort(t)
		baseURL = "http://127.0.0.1:" + port
		parent.Env = append(parent.Env, "PORT="+port)
	}
	parent.Stdout = parentStdout
	parent.Stderr = parentStderr
	if err := parent.Start(); err != nil {
		t.Fatalf("start recovery parent: %v", err)
	}
	if distribution == buildinfo.DistributionDesktop {
		baseURL = waitForDesktopBaseURLFromLogs(t, readParentLogs)
		parsed, err := url.Parse(baseURL)
		if err != nil || parsed.Port() == "" {
			t.Fatalf("parse recovery desktop URL %q: %v", baseURL, err)
		}
		port = parsed.Port()
	}
	t.Cleanup(func() {
		_ = parent.Process.Kill()
		_, _ = parent.Process.Wait()
		killPort(t, port)
	})
	if distribution == buildinfo.DistributionBinary && wantOutcome == packagedUpdateOutcomeSucceeded {
		waitForHealthVersion(t, baseURL, runningVersion, readParentLogs)
		waitForUpdateState(t, baseURL, StateSucceeded)
		assertPackagedRecoveryOutcome(t, staged, wantOutcome)
		assertGoBuildInfoVersion(t, current, runningVersion)
		return
	}
	if err := waitForCommandExit(parent, 90*time.Second); err != nil {
		t.Fatalf("recovery parent did not complete production handoff: %v\nhelper state:\n%s\n%s", err, describePackagedUpdateHelperState(current), readParentLogs())
	}

	wantVersion := runningVersion
	if wantOutcome == packagedUpdateOutcomeRolledBack {
		wantVersion = staged.PreviousVersion
	}
	waitForHealthVersionWithin(t, baseURL, wantVersion, 90*time.Second, func() string {
		return fmt.Sprintf("\nhelper state:\n%s\nparent logs:\n%s", describePackagedUpdateHelperState(current), readParentLogs())
	})
	wantState := StateSucceeded
	if wantOutcome == packagedUpdateOutcomeRolledBack {
		wantState = StateRolledBack
	}
	waitForUpdateState(t, baseURL, wantState)
	assertPackagedRecoveryOutcome(t, staged, wantOutcome)
	assertGoBuildInfoVersion(t, current, wantVersion)
}

func preparePackagedRecoveryPersistence(t *testing.T, appData string, staged LocalStagedUpdate, distribution, currentVersion string) {
	t.Helper()
	if err := os.MkdirAll(appData, 0o755); err != nil {
		t.Fatal(err)
	}
	drain := NewDrainManager(nil, nil, 0, nil)
	if err := drain.SetPersistence(filepath.Join(appData, "update-drain.json")); err != nil {
		t.Fatal(err)
	}
	coordinator := NewCoordinator(nil, CurrentBuild{Build: buildinfo.Build{Version: currentVersion}, Distribution: distribution}, "stable", drain, nil, false, "", nil)
	if err := coordinator.SetPersistence(filepath.Join(appData, "update-coordinator.json")); err != nil {
		t.Fatal(err)
	}
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil || !drain.TakeOwnership(status.Generation) {
		t.Fatalf("prepare recovery drain: status=%#v err=%v", status, err)
	}
	coordinator.mu.Lock()
	coordinator.state = StateRestarting
	coordinator.staged = staged
	coordinator.operationGeneration = status.Generation
	if err := coordinator.persistLocked(); err != nil {
		coordinator.mu.Unlock()
		t.Fatal(err)
	}
	coordinator.mu.Unlock()
}

func assertPackagedRecoveryOutcome(t *testing.T, staged LocalStagedUpdate, want string) {
	t.Helper()
	outcome, err := readPackagedUpdateHelperOutcome(staged)
	if err != nil {
		t.Fatalf("read recovery helper outcome: %v\n%s", err, describePackagedUpdateHelperState(staged.InstallPath))
	}
	if outcome.State != want {
		t.Fatalf("recovery helper outcome = %q, want %q\n%s", outcome.State, want, describePackagedUpdateHelperState(staged.InstallPath))
	}
}

func desktopTestEnvironment() []string {
	if runtime.GOOS == "darwin" {
		return nil
	}
	return []string{
		"OPENVIBELY_UPDATE_E2E_HEADLESS_DESKTOP=1",
	}
}

func testPackagedUpdateE2EAppBundleUpdateHelperSucceeds(t *testing.T) {
	runAppBundleUpdateHelperE2E(t, "0.6.0", "0.6.0", packagedUpdateOutcomeSucceeded)
}

func testPackagedUpdateE2EAppBundleUpdateHelperRollsBack(t *testing.T) {
	runAppBundleUpdateHelperE2E(t, "0.6.0", "0.7.0", packagedUpdateOutcomeRolledBack)
}

func testPackagedUpdateE2ERealDesktopSucceeds(t *testing.T) {
	requireRealDesktopUpdateE2E(t)
	runRealDesktopUpdateE2E(t, "0.6.0", "0.6.0", packagedUpdateOutcomeSucceeded)
}

func testPackagedUpdateE2ERealDesktopRollsBack(t *testing.T) {
	requireRealDesktopUpdateE2E(t)
	runRealDesktopUpdateE2E(t, "0.6.0", "0.7.0", packagedUpdateOutcomeRolledBack)
}

func testPackagedUpdateE2EDesktopRecoverySucceeds(t *testing.T) {
	requireRealDesktopUpdateE2E(t)
	runDesktopRecoveryProcessE2E(t, "0.6.0", packagedUpdateOutcomeSucceeded)
}

func testPackagedUpdateE2EDesktopRecoveryRollsBack(t *testing.T) {
	requireRealDesktopUpdateE2E(t)
	runDesktopRecoveryProcessE2E(t, "0.7.0", packagedUpdateOutcomeRolledBack)
}

func requireRealDesktopUpdateE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("OPENVIBELY_UPDATE_E2E_REAL_DESKTOP") != "1" {
		t.Skip("OPENVIBELY_UPDATE_E2E_REAL_DESKTOP=1 is required for real desktop app update E2E")
	}
}

func runAppBundleUpdateHelperE2E(t *testing.T, expectedVersion, replacementVersion, wantOutcome string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("non-macOS desktop executable updates are covered by real desktop E2E")
	}
	root := t.TempDir()
	currentExe := filepath.Join(root, "current", executableName("openvibely-e2e-fixture"))
	replacementExe := filepath.Join(root, "replacement", executableName("openvibely-e2e-fixture"))
	buildGoCommand(t, "./internal/update/testfixture", currentExe, map[string]string{"main.version": "0.5.0"})
	buildGoCommand(t, "./internal/update/testfixture", replacementExe, map[string]string{"main.version": replacementVersion})

	installPath, installedExecutable, executableRelative := installDesktopUnit(t, root, "OpenVibely.app", currentExe)
	artifact, filetype, filename, kind := packageDesktopArtifact(t, replacementExe)
	publicKeyFile, privateKey := writeE2ETrustRoot(t, root)
	updateServer := serveSignedRelease(t, artifact, filetype, filename, kind, expectedVersion, privateKey)
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
	if err := writePackagedUpdateHelperOutcome(staged, packagedUpdateOutcomeAuthorized); err != nil {
		t.Fatalf("write helper authorization: %v", err)
	}
	helperPath := packagedUpdateHelperPath(staged.InstallPath, AppBundleUpdateHelperCommand)
	if err := copyFile(installedExecutable, helperPath, 0o755); err != nil {
		t.Fatalf("publish app-bundle update helper fixture: %v", err)
	}

	port := freeTCPPort(t)
	t.Cleanup(func() {
		killExecutable(t, installedExecutable)
		killPort(t, port)
	})
	parentPID := exitedCommandPID(t)
	helperArgs := []string{
		AppBundleUpdateHelperCommand,
		"--parent-pid", strconv.Itoa(parentPID),
		"--current", staged.InstallPath,
		"--staged", staged.ArtifactPath,
		"--backup", staged.BackupPath,
		"--health-url", "http://127.0.0.1:" + port + "/health",
		"--expected-version", staged.Version,
		"--previous-version", staged.PreviousVersion,
		"--outcome-id", staged.OutcomeID,
	}
	metadata, err := json.Marshal(packagedUpdateRelaunchMetadata{
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
		"OPENVIBELY_UPDATE_INTEGRATION_VALIDATION_TIMEOUT_MS=15000",
	)
	if wantOutcome == packagedUpdateOutcomeSucceeded {
		cmd.Env = append(cmd.Env, "OPENVIBELY_UPDATE_INTEGRATION_EXIT_AFTER_HEALTH=1")
	}
	output, helperErr := cmd.CombinedOutput()
	if helperErr != nil && wantOutcome != packagedUpdateOutcomeRolledBack {
		t.Fatalf("app-bundle update helper failed: %v\n%s", helperErr, output)
	}
	outcome, err := readPackagedUpdateHelperOutcome(staged)
	if err != nil {
		t.Fatalf("read app-bundle update helper outcome: %v\nhelper err: %v\n%s", err, helperErr, output)
	}
	if outcome.State != wantOutcome {
		t.Fatalf("desktop outcome = %q, want %q\nhelper err: %v\n%s", outcome.State, wantOutcome, helperErr, output)
	}
	if wantOutcome == packagedUpdateOutcomeSucceeded {
		assertInstalledFixtureVersion(t, installedExecutable, replacementVersion)
	} else {
		assertInstalledFixtureVersion(t, installedExecutable, "0.5.0")
	}
}

func runRealDesktopUpdateE2E(t *testing.T, expectedVersion, replacementVersion, wantOutcome string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		runDesktopExecutableUpdateE2E(t, expectedVersion, replacementVersion, wantOutcome)
		return
	}
	root := t.TempDir()
	currentExe := filepath.Join(root, "current", executableName("openvibely-desktop-real"))
	replacementExe := filepath.Join(root, "replacement", executableName("openvibely-desktop-real"))
	buildDesktopCommand(t, currentExe, "0.5.0")
	buildDesktopCommand(t, replacementExe, replacementVersion)

	installPath, installedExecutable, executableRelative := installDesktopUnit(t, root, "OpenVibely.app", currentExe)
	artifact, filetype, filename, kind := packageDesktopArtifact(t, replacementExe)
	publicKeyFile, privateKey := writeE2ETrustRoot(t, root)
	updateServer := serveSignedRelease(t, artifact, filetype, filename, kind, expectedVersion, privateKey)
	defer updateServer.Close()
	updateKeys, err := DecodePublicKeys("", "", publicKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(ClientConfig{
		ServiceURL: updateServer.URL,
		Channel:    "stable",
		StatePath:  filepath.Join(root, "real-desktop-update-state.json"),
		PublicKeys: updateKeys,
	})
	current := CurrentBuild{
		Build:        buildinfo.Build{Version: "0.5.0", OS: runtime.GOOS, Arch: runtime.GOARCH},
		Distribution: buildinfo.DistributionDesktop,
	}
	release, checked, err := client.CheckIfDue(context.Background(), current)
	if err != nil {
		t.Fatalf("real desktop update check: %v", err)
	}
	if !checked || release == nil {
		t.Fatal("real desktop update check did not return a release")
	}
	dataRoot := filepath.Join(root, "real-desktop-data")
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
		t.Fatalf("real desktop stage: %v", err)
	}
	staged, ok := stagedValue.(LocalStagedUpdate)
	if !ok {
		t.Fatalf("real desktop staged value = %T", stagedValue)
	}
	if err := retainDesktopInstallUnit(staged, []string{dataRoot}); err != nil {
		t.Fatalf("retain real desktop install unit: %v", err)
	}
	if err := writePackagedUpdateHelperOutcome(staged, packagedUpdateOutcomeAuthorized); err != nil {
		t.Fatalf("write real app-bundle update helper authorization: %v", err)
	}

	port := freeTCPPort(t)
	t.Cleanup(func() {
		killExecutable(t, installedExecutable)
		killPort(t, port)
	})
	configFile := writeRealDesktopConfig(t, root, dataRoot, updateServer.URL, publicKeyFile)
	helperPath := packagedUpdateHelperPath(staged.InstallPath, AppBundleUpdateHelperCommand)
	if runPackagedUpdateHelperInPlace(runtime.GOOS, staged.InstallPath) {
		helperPath = installedExecutable
	} else if err := copyFile(installedExecutable, helperPath, 0o755); err != nil {
		t.Fatalf("publish real app-bundle update helper: %v", err)
	}
	parentPID := exitedCommandPID(t)
	helperArgs := []string{
		AppBundleUpdateHelperCommand,
		"--parent-pid", strconv.Itoa(parentPID),
		"--current", staged.InstallPath,
		"--staged", staged.ArtifactPath,
		"--backup", staged.BackupPath,
		"--health-url", "http://127.0.0.1:" + port + "/api/system/health",
		"--expected-version", staged.Version,
		"--previous-version", staged.PreviousVersion,
		"--outcome-id", staged.OutcomeID,
	}
	metadata, err := json.Marshal(packagedUpdateRelaunchMetadata{
		Arguments:          []string{installedExecutable},
		WorkingDirectory:   root,
		ExecutableRelative: executableRelative,
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(helperPath, helperArgs...)
	cmd.Stdin = bytes.NewReader(metadata)
	cmd.Env = append(os.Environ(),
		"HOME="+filepath.Join(root, "home"),
		"PORT="+port,
		"OPENVIBELY_DESKTOP_CONFIG_FILE="+configFile,
		"OPENVIBELY_UPDATE_INTEGRATION_WAIT_TIMEOUT_MS=10000",
		"OPENVIBELY_UPDATE_INTEGRATION_VALIDATION_TIMEOUT_MS=15000",
	)
	output, helperErr := cmd.CombinedOutput()
	if helperErr != nil && wantOutcome != packagedUpdateOutcomeRolledBack {
		t.Fatalf("real app-bundle update helper failed: %v\n%s", helperErr, output)
	}
	outcome, err := readPackagedUpdateHelperOutcome(staged)
	if err != nil {
		t.Fatalf("read real app-bundle update helper outcome: %v\nhelper err: %v\n%s", err, helperErr, output)
	}
	if outcome.State != wantOutcome {
		t.Fatalf("real desktop outcome = %q, want %q\nhelper err: %v\n%s", outcome.State, wantOutcome, helperErr, output)
	}
	if wantOutcome == packagedUpdateOutcomeSucceeded {
		assertGoBuildInfoVersion(t, installedExecutable, replacementVersion)
		assertNoDesktopHalfSwap(t, staged)
	} else {
		assertGoBuildInfoVersion(t, installedExecutable, "0.5.0")
	}
}

func runDesktopRecoveryProcessE2E(t *testing.T, runningVersion, wantOutcome string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		runExecutableRecoveryProcessE2E(t, buildinfo.DistributionDesktop, runningVersion, wantOutcome)
		return
	}

	root := t.TempDir()
	predecessor := filepath.Join(root, "build", "predecessor", "OpenVibely")
	replacement := filepath.Join(root, "build", "replacement", "OpenVibely")
	buildDesktopCommand(t, predecessor, "0.5.0")
	buildDesktopCommand(t, replacement, runningVersion)
	installPath, installedExecutable, _ := installDesktopUnit(t, root, "OpenVibely.app", replacement)
	stagedPath, _, _ := installDesktopUnit(t, root, "OpenVibely.app.openvibely-new", predecessor)
	backupPath, _, _ := installDesktopUnit(t, root, "OpenVibely.app.openvibely-backup", predecessor)
	staged := LocalStagedUpdate{
		ArtifactPath:    stagedPath,
		InstallPath:     installPath,
		BackupPath:      backupPath,
		Version:         "0.6.0",
		PreviousVersion: "0.5.0",
		OutcomeID:       "e2e-recovery-desktop-" + wantOutcome,
	}
	if err := writePackagedUpdateHelperPhase(staged, packagedUpdateOutcomeTargetPublished); err != nil {
		t.Fatal(err)
	}

	port := freeTCPPort(t)
	baseURL := "http://127.0.0.1:" + port
	dataRoot := filepath.Join(root, "desktop-data")
	preparePackagedRecoveryPersistence(t, dataRoot, staged, buildinfo.DistributionDesktop, runningVersion)
	publicKeyFile, _ := writeE2ETrustRoot(t, root)
	configFile := writeRealDesktopConfig(t, root, dataRoot, "http://127.0.0.1:1", publicKeyFile)
	processEnv := append(envWithout("PORT"),
		"HOME="+filepath.Join(root, "home"),
		"PORT="+port,
		"OPENVIBELY_DESKTOP_CONFIG_FILE="+configFile,
		"OPENVIBELY_UPDATE_INTEGRATION_WAIT_TIMEOUT_MS=10000",
		"OPENVIBELY_UPDATE_INTEGRATION_VALIDATION_TIMEOUT_MS=15000",
	)
	parentStdout, parentStderr, readParentLogs := openCommandLogs(t, root, "recovery-parent")
	parent := exec.Command(installedExecutable)
	parent.Dir = root
	parent.Env = processEnv
	parent.Stdout = parentStdout
	parent.Stderr = parentStderr
	if err := parent.Start(); err != nil {
		t.Fatalf("start app-bundle recovery parent: %v", err)
	}
	t.Cleanup(func() {
		_ = parent.Process.Kill()
		killExecutable(t, installedExecutable)
		killPort(t, port)
	})
	if err := waitForCommandExit(parent, 90*time.Second); err != nil {
		coordinatorState, _ := os.ReadFile(filepath.Join(dataRoot, "update-coordinator.json"))
		drainState, _ := os.ReadFile(filepath.Join(dataRoot, "update-drain.json"))
		t.Fatalf("app-bundle recovery parent did not complete production handoff: %v\nupdate snapshot: %s\ncoordinator state: %s\ndrain state: %s\nhelper state:\n%s\n%s", err, readUpdateSnapshot(baseURL), coordinatorState, drainState, describePackagedUpdateHelperState(installPath), readParentLogs())
	}

	wantVersion := runningVersion
	if wantOutcome == packagedUpdateOutcomeRolledBack {
		wantVersion = staged.PreviousVersion
	}
	waitForHealthVersionWithin(t, baseURL, wantVersion, 90*time.Second, func() string {
		return fmt.Sprintf("\nhelper state:\n%s\nparent logs:\n%s", describePackagedUpdateHelperState(installPath), readParentLogs())
	})
	wantState := StateSucceeded
	if wantOutcome == packagedUpdateOutcomeRolledBack {
		wantState = StateRolledBack
	}
	waitForUpdateState(t, baseURL, wantState)
	assertPackagedRecoveryOutcome(t, staged, wantOutcome)
	assertGoBuildInfoVersion(t, installedExecutable, wantVersion)
}

func runDesktopExecutableUpdateE2E(t *testing.T, releaseVersion, replacementVersion, wantOutcome string) {
	t.Helper()
	root := t.TempDir()
	current := filepath.Join(root, executableName("openvibely-desktop"))
	replacement := filepath.Join(root, "replacement", executableName("openvibely-desktop"))
	buildDesktopCommand(t, current, "0.5.0")
	buildDesktopCommand(t, replacement, replacementVersion)
	archive, filetype, filename, kind := packageDesktopArtifact(t, replacement)
	publicKeyFile, privateKey := writeE2ETrustRoot(t, root)
	updateServer := serveSignedRelease(t, archive, filetype, filename, kind, releaseVersion, privateKey)
	defer updateServer.Close()

	appData := filepath.Join(root, "desktop-data")
	stdoutLog, stderrLog, readLogs := openCommandLogs(t, root, "desktop-current")
	helperLogPath := filepath.Join(root, "desktop-update-helper.log")
	readDiagnostics := func() string {
		helperLog, err := os.ReadFile(helperLogPath)
		if err != nil {
			return fmt.Sprintf("%s\nhelper log: %v", readLogs(), err)
		}
		return fmt.Sprintf("%s\nhelper log:\n%s", readLogs(), helperLog)
	}
	cmd := exec.Command(current)
	cmd.Dir = root
	cmd.Env = append(envWithout("PORT"),
		"DATABASE_PATH="+filepath.Join(appData, "openvibely.db"),
		"PROJECT_REPO_ROOT="+filepath.Join(root, "projects"),
		"OPENVIBELY_APP_DATA_DIR="+appData,
		"OPENVIBELY_PLUGIN_ROOT="+filepath.Join(appData, "plugins"),
		"OPENVIBELY_UPDATE_SERVICE_URL="+updateServer.URL,
		"OPENVIBELY_UPDATE_PUBLIC_KEY_FILE="+publicKeyFile,
		"DISABLE_UPDATE_NOTIFICATIONS=false",
		"OPENVIBELY_DISABLE_INSTALL_ID=1",
		updateIntegrationHelperLogEnv+"="+helperLogPath,
	)
	cmd.Env = append(cmd.Env, desktopTestEnvironment()...)
	cmd.Stdout = stdoutLog
	cmd.Stderr = stderrLog
	if err := cmd.Start(); err != nil {
		t.Fatalf("start current desktop app: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	baseURL := waitForDesktopBaseURLFromLogs(t, readLogs)
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil || parsedBaseURL.Port() == "" {
		t.Fatalf("parse desktop backend URL %q: %v", baseURL, err)
	}
	t.Cleanup(func() { killPort(t, parsedBaseURL.Port()) })
	waitForHealthVersion(t, baseURL, "0.5.0")
	waitForStagedUpdate(t, baseURL)
	resp, err := http.Post(baseURL+"/api/system/update/apply", "application/json", nil)
	if err != nil {
		t.Fatalf("accept desktop update: %v\n%s", err, readDiagnostics())
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("accept desktop update HTTP %d\n%s", resp.StatusCode, readDiagnostics())
	}
	if err := waitForCommandExit(cmd, time.Minute); err != nil {
		t.Fatalf("current desktop app exit after handoff: %v\nupdate snapshot: %s\nhelper state:\n%s\n%s", err, readUpdateSnapshot(baseURL), describePackagedUpdateHelperState(current), readDiagnostics())
	}
	if wantOutcome == packagedUpdateOutcomeSucceeded {
		waitForHealthVersionWithin(t, baseURL, releaseVersion, 120*time.Second, func() string {
			return fmt.Sprintf("\nupdate snapshot: %s\nhelper state:\n%s\n%s", readUpdateSnapshot(baseURL), describePackagedUpdateHelperState(current), readDiagnostics())
		})
	} else {
		waitForHealthVersionWithin(t, baseURL, "0.5.0", 120*time.Second, func() string {
			return fmt.Sprintf("\nupdate snapshot: %s\nhelper state:\n%s\n%s", readUpdateSnapshot(baseURL), describePackagedUpdateHelperState(current), readDiagnostics())
		})
	}
	waitForUpdateState(t, baseURL, wantOutcome)
}

func envWithout(keys ...string) []string {
	blocked := make(map[string]bool, len(keys))
	for _, key := range keys {
		blocked[key] = true
	}
	env := os.Environ()
	filtered := env[:0]
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if ok && blocked[key] {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append([]string(nil), filtered...)
}

func waitForDesktopBaseURLFromLogs(t *testing.T, readLogs func() string) string {
	t.Helper()
	const marker = "[desktop] backend listening at "
	deadline := time.Now().Add(45 * time.Second)
	var logs string
	for time.Now().Before(deadline) {
		logs = readLogs()
		if index := strings.Index(logs, marker); index >= 0 {
			value := strings.TrimSpace(logs[index+len(marker):])
			if lineEnd := strings.IndexAny(value, "\r\n"); lineEnd >= 0 {
				value = strings.TrimSpace(value[:lineEnd])
			}
			if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
				return value
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("desktop backend URL was not logged\n%s", logs)
	return ""
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

func readUpdateSnapshot(baseURL string) string {
	resp, err := http.Get(baseURL + "/api/system/update")
	if err != nil {
		return err.Error()
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return fmt.Sprintf("HTTP %d: %v", resp.StatusCode, err)
	}
	return fmt.Sprintf("HTTP %d: %s", resp.StatusCode, body)
}

func describePackagedUpdateHelperState(current string) string {
	staged := LocalStagedUpdate{InstallPath: current}
	paths := []string{
		packagedUpdateHelperPreparedPath(current),
		packagedUpdateHelperOutcomePath(current),
		packagedUpdateHelperAuthorizedPath(current),
		packagedUpdateHelperCancelledPath(current),
		packagedUpdateHelperRecoveryClaimPath(current),
		packagedUpdateHelperRecoveryReadyPath(current),
		packagedUpdateHelperRelaunchMetadataPath(current),
		packagedUpdateHelperPath(current, ExecutableUpdateHelperCommand),
	}
	var state strings.Builder
	var identity packagedUpdateHelperOutcome
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(&state, "%s: %v\n", filepath.Base(path), err)
			continue
		}
		fmt.Fprintf(&state, "%s: %d bytes", filepath.Base(path), info.Size())
		if strings.HasSuffix(path, ".json") && path != packagedUpdateHelperRelaunchMetadataPath(current) {
			if data, readErr := os.ReadFile(path); readErr == nil {
				fmt.Fprintf(&state, " %s", data)
				if identity.ID == "" {
					_ = json.Unmarshal(data, &identity)
				}
			}
		}
		state.WriteByte('\n')
	}
	if identity.ID != "" {
		staged.OutcomeID = identity.ID
		staged.PreviousVersion = identity.PreviousVersion
		staged.Version = identity.DesiredVersion
		if outcome, err := readPackagedUpdateHelperOutcome(staged); err == nil {
			fmt.Fprintf(&state, "semantic outcome: %s\n", outcome.State)
		} else {
			fmt.Fprintf(&state, "semantic outcome: %v\n", err)
		}
		leasePath := packagedUpdateHelperLeasePath(staged)
		if _, err := os.Stat(leasePath); err != nil {
			fmt.Fprintf(&state, "%s: %v\n", filepath.Base(leasePath), err)
		} else if lease, acquired, err := tryAcquirePackagedUpdateHelperLease(leasePath); err != nil {
			fmt.Fprintf(&state, "%s: %v\n", filepath.Base(leasePath), err)
		} else if acquired {
			fmt.Fprintf(&state, "%s: not held\n", filepath.Base(leasePath))
			_ = lease.Close()
		} else {
			fmt.Fprintf(&state, "%s: held\n", filepath.Base(leasePath))
		}
	}
	return state.String()
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

func buildDesktopCommand(t *testing.T, output, version string) {
	t.Helper()
	buildGoCommand(t, "./cmd/desktop", output, map[string]string{
		"github.com/openvibely/openvibely/internal/buildinfo.Version":  version,
		"github.com/openvibely/openvibely/internal/buildinfo.Commit":   "e2e-desktop-" + version,
		"github.com/openvibely/openvibely/internal/buildinfo.Artifact": "desktop",
	})
}

func writeRealDesktopConfig(t *testing.T, root, dataRoot, updateURL, publicKeyFile string) string {
	t.Helper()
	configPath := filepath.Join(root, "desktop-config.env")
	lines := []string{
		"OPENVIBELY_APP_DATA_DIR=" + dataRoot,
		"DATABASE_PATH=" + filepath.Join(dataRoot, "openvibely.db"),
		"PROJECT_REPO_ROOT=" + filepath.Join(dataRoot, "repos"),
		"OPENVIBELY_PLUGIN_ROOT=" + filepath.Join(dataRoot, "plugins"),
		"OPENVIBELY_UPDATE_SERVICE_URL=" + updateURL,
		"OPENVIBELY_UPDATE_PUBLIC_KEY_FILE=" + publicKeyFile,
		"DISABLE_UPDATE_NOTIFICATIONS=false",
		"OPENVIBELY_DISABLE_INSTALL_ID=1",
		"AUTH_ENABLED=false",
		"OPENVIBELY_UPDATE_INTEGRATION_WAIT_TIMEOUT_MS=5000",
		"OPENVIBELY_UPDATE_INTEGRATION_VALIDATION_TIMEOUT_MS=5000",
	}
	if err := os.WriteFile(configPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write real desktop config: %v", err)
	}
	return configPath
}

func exitedCommandPID(t *testing.T) int {
	t.Helper()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", "exit", "0")
	} else {
		cmd = exec.Command("sh", "-c", "exit 0")
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start exited command: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait exited command: %v", err)
	}
	return pid
}

func assertNoDesktopHalfSwap(t *testing.T, staged LocalStagedUpdate) {
	t.Helper()
	if _, err := os.Stat(staged.InstallPath); err != nil {
		t.Fatalf("desktop install path missing after update: %v", err)
	}
	if _, err := os.Stat(staged.ArtifactPath); !os.IsNotExist(err) {
		t.Fatalf("desktop update left staged replacement %s: %v", staged.ArtifactPath, err)
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
	entry := executableName("openvibely")
	switch runtime.GOOS {
	case "linux":
		return packageReleaseTarGZ(t, executable, entry), "tar.gz", "openvibely.tar.gz"
	default:
		return packageReleaseZip(t, executable, entry), "zip", "openvibely.zip"
	}
}

func packageDesktopArtifact(t *testing.T, executable string) ([]byte, string, string, string) {
	t.Helper()
	switch runtime.GOOS {
	case "darwin":
		return packageReleaseMacAppZip(t, executable), "zip", "openvibely-desktop.zip", "app_bundle"
	case "linux":
		return packageReleaseTarGZ(t, executable, "openvibely-desktop"), "tar.gz", "openvibely-desktop.tar.gz", "executable"
	default:
		entry := executableName("openvibely-desktop")
		return packageReleaseZip(t, executable, entry), "zip", "openvibely-desktop.zip", "executable"
	}
}

func packageReleaseTarGZ(t *testing.T, executable, entry string) []byte {
	t.Helper()
	root := t.TempDir()
	pkgDir := filepath.Join(root, "pkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	payloadPath := filepath.Join(pkgDir, entry)
	copyFile(executable, payloadPath, 0o755)
	addPackagedUpdateXattr(t, payloadPath)
	archivePath := filepath.Join(root, "artifact.tar.gz")
	cmd := exec.Command("tar", "-czf", "artifact.tar.gz", "-C", "pkg", entry)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "COPYFILE_DISABLE=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("release tar package failed: %v\n%s", err, output)
	}
	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	assertCleanArchiveEntries(t, archivePath, []string{entry})
	return data
}

func packageReleaseZip(t *testing.T, executable, entry string) []byte {
	t.Helper()
	root := t.TempDir()
	pkgDir := filepath.Join(root, "pkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	payloadPath := filepath.Join(pkgDir, entry)
	copyFile(executable, payloadPath, 0o755)
	addPackagedUpdateXattr(t, payloadPath)
	archivePath := filepath.Join(root, "artifact.zip")
	if zipPath, err := exec.LookPath("zip"); err == nil {
		cmd := exec.Command(zipPath, "-X", archivePath, entry)
		cmd.Dir = pkgDir
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("release zip package failed: %v\n%s", err, output)
		}
	} else {
		writeGoZip(t, archivePath, map[string]string{entry: payloadPath})
	}
	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	assertCleanArchiveEntries(t, archivePath, []string{entry})
	return data
}

func packageReleaseMacAppZip(t *testing.T, executable string) []byte {
	t.Helper()
	root := t.TempDir()
	appDir := filepath.Join(root, "OpenVibely.app")
	executablePath := filepath.Join(appDir, "Contents", "MacOS", "OpenVibely")
	infoPath := filepath.Join(appDir, "Contents", "Info.plist")
	if err := os.MkdirAll(filepath.Dir(executablePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(infoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	copyFile(executable, executablePath, 0o755)
	if err := os.WriteFile(infoPath, []byte(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>CFBundleExecutable</key><string>OpenVibely</string></dict></plist>`), 0o644); err != nil {
		t.Fatal(err)
	}
	addPackagedUpdateXattr(t, executablePath)
	addPackagedUpdateXattr(t, infoPath)
	archivePath := filepath.Join(root, "OpenVibely.app.zip")
	if runtime.GOOS == "darwin" {
		cmd := exec.Command("ditto", "-c", "-k", "--norsrc", "--keepParent", appDir, archivePath)
		cmd.Env = append(os.Environ(), "COPYFILE_DISABLE=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("release macOS app package failed: %v\n%s", err, output)
		}
	} else {
		writeGoZip(t, archivePath, map[string]string{
			"OpenVibely.app/Contents/Info.plist":       infoPath,
			"OpenVibely.app/Contents/MacOS/OpenVibely": executablePath,
		})
	}
	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	assertCleanArchiveEntries(t, archivePath, []string{
		"OpenVibely.app/Contents/Info.plist",
		"OpenVibely.app/Contents/MacOS/OpenVibely",
	})
	return data
}

func TestReleaseShapedPackagingProducesCleanArchives(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "openvibely-source")
	desktop := filepath.Join(root, "openvibely-desktop-source")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(desktop, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = packageReleaseTarGZ(t, binary, "openvibely")
	_ = packageReleaseTarGZ(t, desktop, "openvibely-desktop")
	_ = packageReleaseZip(t, binary, executableName("openvibely"))
	_ = packageReleaseZip(t, desktop, executableName("openvibely-desktop"))
	_ = packageReleaseMacAppZip(t, desktop)
}

func writeGoZip(t *testing.T, archivePath string, entries map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, source := range entries {
		payload, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		writeZipEntry(t, zw, name, 0o755, payload)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func addPackagedUpdateXattr(t *testing.T, file string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		return
	}
	cmd := exec.Command("xattr", "-w", "com.openvibely.update-test", "metadata", file)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Logf("unable to add xattr to %s: %v\n%s", file, err, output)
	}
}

func assertCleanArchiveEntries(t *testing.T, archivePath string, required []string) {
	t.Helper()
	entries := archiveEntries(t, archivePath)
	seen := map[string]bool{}
	for _, entry := range entries {
		clean := strings.TrimPrefix(path.Clean(filepath.ToSlash(entry)), "./")
		base := path.Base(clean)
		if base == ".DS_Store" || strings.HasPrefix(base, "._") || strings.HasPrefix(clean, "__MACOSX/") {
			t.Fatalf("archive %s contains metadata entry %q; entries=%v", archivePath, entry, entries)
		}
		if !strings.HasSuffix(clean, "/") {
			seen[clean] = true
		}
	}
	for _, want := range required {
		if !seen[want] {
			t.Fatalf("archive %s missing %q; entries=%v", archivePath, want, entries)
		}
	}
}

func archiveEntries(t *testing.T, archivePath string) []string {
	t.Helper()
	if strings.HasSuffix(archivePath, ".tar.gz") {
		file, err := os.Open(archivePath)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		gz, err := gzip.NewReader(file)
		if err != nil {
			t.Fatal(err)
		}
		defer gz.Close()
		reader := tar.NewReader(gz)
		var entries []string
		for {
			header, err := reader.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			entries = append(entries, header.Name)
		}
		return entries
	}
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	entries := make([]string, 0, len(zr.File))
	for _, file := range zr.File {
		entries = append(entries, file.Name)
	}
	return entries
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

func waitForHealthVersion(t *testing.T, baseURL, version string, diagnostics ...func() string) {
	t.Helper()
	waitForHealthVersionWithin(t, baseURL, version, 45*time.Second, diagnostics...)
}

func waitForHealthVersionWithin(t *testing.T, baseURL, version string, timeout time.Duration, diagnostics ...func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
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
	var details strings.Builder
	for _, diagnostic := range diagnostics {
		if diagnostic != nil {
			details.WriteString(diagnostic())
		}
	}
	t.Fatalf("health did not report version %s: last=%s%s", version, last, details.String())
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

func waitForStagedUpdate(t *testing.T, baseURL string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/api/system/update")
		if err == nil {
			var body struct {
				State  string `json:"state"`
				Error  string `json:"error"`
				Staged bool   `json:"staged"`
			}
			if resp.Body != nil {
				_ = json.NewDecoder(resp.Body).Decode(&body)
				_ = resp.Body.Close()
			}
			if resp.StatusCode == http.StatusOK && body.State == StateAvailable && body.Staged {
				return
			}
			last = fmt.Sprintf("HTTP %d state %q staged=%t error %q", resp.StatusCode, body.State, body.Staged, body.Error)
		} else {
			last = err.Error()
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("update replacement was not staged: last=%s", last)
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

func assertBinarySymlinkLayoutUpdated(t *testing.T, commandDir, commandPath, targetPath, releaseVersion, wantState string) {
	t.Helper()
	if linkTarget, err := os.Readlink(commandPath); err != nil {
		t.Fatalf("command path is not a symlink after update: %v", err)
	} else if linkTarget != targetPath {
		t.Fatalf("command symlink target = %q, want %q", linkTarget, targetPath)
	}
	for _, suffix := range []string{".openvibely-package", ".openvibely-new", ".openvibely-backup"} {
		if _, err := os.Stat(commandPath + suffix); !os.IsNotExist(err) {
			t.Fatalf("updater wrote staging artifact beside command symlink %s: %v", commandPath+suffix, err)
		}
	}
	entries, err := os.ReadDir(commandDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".openvibely-") {
			t.Fatalf("updater wrote staging artifact in command dir: %s", entry.Name())
		}
	}
	if wantState == StateSucceeded {
		assertGoBuildInfoVersion(t, targetPath, releaseVersion)
	}
}

func assertGoBuildInfoVersion(t *testing.T, executable, version string) {
	t.Helper()
	cmd := exec.Command("go", "version", "-m", executable)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go version -m %s: %v\n%s", executable, err, output)
	}
	if !bytes.Contains(output, []byte("github.com/openvibely/openvibely/internal/buildinfo.Version="+version)) {
		t.Fatalf("installed binary does not contain version %s\n%s", version, output)
	}
}

func killPort(t *testing.T, port string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
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
	out, err := exec.CommandContext(ctx, "lsof", "-tiTCP:"+port, "-sTCP:LISTEN").Output()
	if err != nil {
		return
	}
	terminateUnixProcessTrees(ctx, unixProcessTreePIDs(ctx, strings.Fields(string(out))))
}

func killExecutable(t *testing.T, executable string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-e", "-o", "pid=", "-o", "args=").Output()
	if err != nil {
		return
	}
	var roots []string
	for _, line := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			continue
		}
		commandLine := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
		if commandLine == executable || strings.HasPrefix(commandLine, executable+" ") {
			roots = append(roots, fields[0])
		}
	}
	terminateUnixProcessTrees(ctx, unixProcessTreePIDs(ctx, roots))
}

func terminateUnixProcessTrees(ctx context.Context, pids []string) {
	for _, pid := range pids {
		_ = exec.CommandContext(ctx, "kill", "-TERM", pid).Run()
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		alive := false
		for _, pid := range pids {
			if exec.CommandContext(ctx, "kill", "-0", pid).Run() == nil {
				alive = true
				break
			}
		}
		if !alive {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, pid := range pids {
		_ = exec.CommandContext(ctx, "kill", "-KILL", pid).Run()
	}
}

func unixProcessTreePIDs(ctx context.Context, roots []string) []string {
	out, err := exec.CommandContext(ctx, "ps", "-e", "-o", "pid=", "-o", "ppid=").Output()
	if err != nil {
		return roots
	}
	children := make(map[string][]string)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			children[fields[1]] = append(children[fields[1]], fields[0])
		}
	}
	seen := make(map[string]bool)
	ordered := make([]string, 0, len(roots))
	var appendTree func(string)
	appendTree = func(pid string) {
		if seen[pid] {
			return
		}
		seen[pid] = true
		for _, child := range children[pid] {
			appendTree(child)
		}
		ordered = append(ordered, pid)
	}
	for _, root := range roots {
		appendTree(root)
	}
	return ordered
}
