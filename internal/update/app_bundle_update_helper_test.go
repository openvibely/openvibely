package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func appBundleUpdateHelperFixture(t *testing.T) LocalStagedUpdate {
	t.Helper()
	root := t.TempDir()
	current := filepath.Join(root, "openvibely-desktop")
	staged := LocalStagedUpdate{InstallPath: current, ArtifactPath: current + ".openvibely-new", BackupPath: current + ".openvibely-backup", Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "desktop-operation-1"}
	for path, contents := range map[string]string{staged.InstallPath: "old", staged.ArtifactPath: "new", staged.BackupPath: "old"} {
		if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := writePackagedUpdateHelperPhase(staged, packagedUpdateOutcomeAuthorized); err != nil {
		t.Fatal(err)
	}
	return staged
}

func TestAppBundleUpdateHelperValidatesHealthAndRollsBackFailedSuccessor(t *testing.T) {
	staged := appBundleUpdateHelperFixture(t)
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ready":true,"version":"wrong"}`))
	}))
	defer health.Close()
	var starts, stops atomic.Int32
	err := RunAppBundleUpdateHelper(context.Background(), AppBundleUpdateHelperConfig{
		ParentPID: 99999999, Current: staged.InstallPath, Staged: staged.ArtifactPath, Backup: staged.BackupPath,
		HealthURL: health.URL, ExpectedVersion: staged.Version, PreviousVersion: staged.PreviousVersion, OutcomeID: staged.OutcomeID,
		WaitTimeout: 100 * time.Millisecond, ValidationTimeout: 30 * time.Millisecond,
		StartCommand: func() (func(context.Context) error, error) {
			starts.Add(1)
			return func(context.Context) error { stops.Add(1); return nil }, nil
		},
	})
	if err == nil {
		t.Fatal("app-bundle update helper accepted the wrong health version")
	}
	if starts.Load() != 2 || stops.Load() != 1 {
		t.Fatalf("starts=%d stops=%d, want rollback relaunch after one failed-successor stop", starts.Load(), stops.Load())
	}
	if data, readErr := os.ReadFile(staged.InstallPath); readErr != nil || string(data) != "old" {
		t.Fatalf("rolled-back desktop = %q, err=%v", data, readErr)
	}
	outcome, readErr := readPackagedUpdateHelperOutcome(staged)
	if readErr != nil || outcome.State != packagedUpdateOutcomeRolledBack {
		t.Fatalf("outcome=%#v err=%v", outcome, readErr)
	}
}

func TestAppBundleUpdateHelperRecoveryDoesNotRepeatAmbiguousCompletedExchange(t *testing.T) {
	staged := appBundleUpdateHelperFixture(t)
	if err := atomicExchangeInstallUnits(staged.InstallPath, staged.ArtifactPath); err != nil {
		t.Fatal(err)
	}
	if err := writePackagedUpdateHelperPhase(staged, packagedUpdateOutcomeParentExited); err != nil {
		t.Fatal(err)
	}
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ready":true,"version":"0.6.0"}`))
	}))
	defer health.Close()
	originalWaitForProcessExit := waitForProcessExit
	t.Cleanup(func() { waitForProcessExit = originalWaitForProcessExit })
	var parentExitObserved atomic.Bool
	waitForProcessExit = func(context.Context, int, time.Duration) error {
		parentExitObserved.Store(true)
		return nil
	}
	if err := RunAppBundleUpdateHelper(context.Background(), AppBundleUpdateHelperConfig{
		ParentPID: 99999999, Current: staged.InstallPath, Staged: staged.ArtifactPath, Backup: staged.BackupPath,
		HealthURL: health.URL, ExpectedVersion: staged.Version, PreviousVersion: staged.PreviousVersion, OutcomeID: staged.OutcomeID,
		Recovery: true, RunningVersion: staged.Version, WaitTimeout: 100 * time.Millisecond, ValidationTimeout: time.Second,
		StartCommand: func() (func(context.Context) error, error) {
			if !parentExitObserved.Load() {
				t.Fatal("recovery relaunched the app bundle before its parent exited")
			}
			return func(context.Context) error { return nil }, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(staged.InstallPath); err != nil || string(data) != "new" {
		t.Fatalf("recovery repeated completed exchange: current=%q err=%v", data, err)
	}
}

func TestAppBundleUpdateHelperResumesJournaledPublishedTargetAndValidates(t *testing.T) {
	staged := appBundleUpdateHelperFixture(t)
	if err := atomicExchangeInstallUnits(staged.InstallPath, staged.ArtifactPath); err != nil {
		t.Fatal(err)
	}
	if err := writePackagedUpdateHelperPhase(staged, packagedUpdateOutcomeTargetPublished); err != nil {
		t.Fatal(err)
	}
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ready":true,"version":"0.6.0"}`))
	}))
	defer health.Close()
	var starts atomic.Int32
	if err := RunAppBundleUpdateHelper(context.Background(), AppBundleUpdateHelperConfig{
		ParentPID: 99999999, Current: staged.InstallPath, Staged: staged.ArtifactPath, Backup: staged.BackupPath,
		HealthURL: health.URL, ExpectedVersion: staged.Version, PreviousVersion: staged.PreviousVersion, OutcomeID: staged.OutcomeID,
		WaitTimeout: 100 * time.Millisecond, ValidationTimeout: time.Second,
		StartCommand: func() (func(context.Context) error, error) {
			starts.Add(1)
			return func(context.Context) error { return nil }, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 1 {
		t.Fatalf("successor starts=%d", starts.Load())
	}
	if data, err := os.ReadFile(staged.InstallPath); err != nil || string(data) != "new" {
		t.Fatalf("published desktop=%q err=%v", data, err)
	}
	outcome, err := readPackagedUpdateHelperOutcome(staged)
	if err != nil || outcome.State != packagedUpdateOutcomeSucceeded {
		t.Fatalf("outcome=%#v err=%v", outcome, err)
	}
}

func TestAppBundleUpdateHelperArgumentAndRelaunchParsingContracts(t *testing.T) {
	cfg, err := ParseAppBundleUpdateHelperArgs([]string{
		"--parent-pid", "1234",
		"--current", "/tmp/current",
		"--staged", "/tmp/current.openvibely-new",
		"--backup", "/tmp/current.openvibely-backup",
		"--health-url", "http://127.0.0.1:3456/health",
		"--expected-version", "0.6.0",
		"--previous-version", "0.5.0",
		"--outcome-id", "outcome-1",
		"--recovery", "true",
		"--running-version", "0.5.0",
	})
	if err != nil {
		t.Fatalf("ParseAppBundleUpdateHelperArgs: %v", err)
	}
	if cfg.ParentPID != 1234 || !cfg.Recovery || cfg.RunningVersion != "0.5.0" || cfg.HealthURL == "" {
		t.Fatalf("parsed config = %#v", cfg)
	}

	for _, args := range [][]string{
		{"--parent-pid"},
		{"--parent-pid", "not-a-pid"},
		{"--parent-pid", "1", "--recovery", "false"},
		{"--parent-pid", "1", "--recovery", "true"},
		{"--unknown", "value"},
		{"--parent-pid", "1", "--parent-pid", "2"},
	} {
		if _, err := ParseAppBundleUpdateHelperArgs(args); err == nil {
			t.Fatalf("ParseAppBundleUpdateHelperArgs(%v) unexpectedly succeeded", args)
		}
	}

	var relaunch AppBundleUpdateHelperConfig
	metadataBytes, err := json.Marshal(packagedUpdateRelaunchMetadata{Arguments: []string{"OpenVibely", "--flag"}, WorkingDirectory: t.TempDir(), ExecutableRelative: "Contents/MacOS/OpenVibely"})
	if err != nil {
		t.Fatal(err)
	}
	metadata := string(metadataBytes)
	if err := LoadAppBundleUpdateHelperRelaunch(strings.NewReader(metadata), &relaunch); err != nil {
		t.Fatalf("LoadAppBundleUpdateHelperRelaunch: %v", err)
	}
	if len(relaunch.Arguments) != 2 || relaunch.Arguments[1] != "--flag" || relaunch.ExecutableRelative == "" {
		t.Fatalf("relaunch config = %#v", relaunch)
	}
	for _, input := range []string{
		`{"arguments":[],"working_directory":"/tmp"}`,
		`{"arguments":["OpenVibely"],"working_directory":"relative"}`,
		`{"arguments":["OpenVibely"],"working_directory":"/tmp","extra":true}`,
		`not-json`,
	} {
		if err := LoadAppBundleUpdateHelperRelaunch(strings.NewReader(input), &AppBundleUpdateHelperConfig{}); err == nil {
			t.Fatalf("LoadAppBundleUpdateHelperRelaunch(%q) unexpectedly succeeded", input)
		}
	}
	if err := LoadAppBundleUpdateHelperRelaunch(nil, &AppBundleUpdateHelperConfig{}); err == nil {
		t.Fatal("nil relaunch reader unexpectedly succeeded")
	}
	if err := LoadAppBundleUpdateHelperRelaunch(strings.NewReader(metadata), nil); err == nil {
		t.Fatal("nil relaunch config unexpectedly succeeded")
	}
}

func TestDesktopSuccessorCommandUsesLaunchServicesForMacAppBundles(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS LaunchServices relaunch behavior")
	}
	root := t.TempDir()
	mockBin := filepath.Join(root, "bin")
	if err := os.MkdirAll(mockBin, 0o700); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(root, "open.capture")
	openPath := filepath.Join(mockBin, "open")
	script := "#!/bin/sh\nprintf 'args:' > \"$OPEN_CAPTURE\"\nfor arg in \"$@\"; do printf '[%s]' \"$arg\" >> \"$OPEN_CAPTURE\"; done\nprintf '\\nPORT=%s\\n' \"${PORT:-}\" >> \"$OPEN_CAPTURE\"\n"
	if err := os.WriteFile(openPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", mockBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPEN_CAPTURE", capture)

	start := appBundleSuccessorCommand(AppBundleUpdateHelperConfig{
		Current:            filepath.Join(root, "OpenVibely.app"),
		HealthURL:          "http://127.0.0.1:54420/api/system/health",
		Arguments:          []string{"OpenVibely", "--flag", "value"},
		WorkingDirectory:   root,
		ExecutableRelative: "Contents/MacOS/OpenVibely",
	})
	stop, err := start()
	if err != nil {
		t.Fatal(err)
	}
	if stop == nil {
		t.Fatal("macOS app bundle relaunch should expose a rollback stopper")
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{
		"args:[-n][" + filepath.Join(root, "OpenVibely.app") + "][--env][PORT=54420]",
		"[--args][--flag][value]",
		"PORT=",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("open invocation = %q, missing %q", got, want)
		}
	}
}

func TestRunAppBundleUpdateHelperEarlyValidationErrors(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "openvibely-desktop")
	staged := current + ".openvibely-new"
	backup := current + ".openvibely-backup"
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []AppBundleUpdateHelperConfig{
		{ParentPID: 999999, Current: "relative", Staged: staged, Backup: backup, HealthURL: "http://127.0.0.1:1/health", ExpectedVersion: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "outcome"},
		{ParentPID: os.Getpid(), Current: current, Staged: staged, Backup: backup, HealthURL: "http://127.0.0.1:1/health", ExpectedVersion: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "outcome"},
		{ParentPID: 999999, Current: current, Staged: staged, Backup: backup, HealthURL: "", ExpectedVersion: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "outcome"},
	} {
		if err := RunAppBundleUpdateHelper(context.Background(), cfg); err == nil {
			t.Fatalf("RunAppBundleUpdateHelper(%#v) unexpectedly succeeded", cfg)
		}
	}
}
