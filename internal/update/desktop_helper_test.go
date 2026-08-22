package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func desktopHelperFixture(t *testing.T) LocalStagedUpdate {
	t.Helper()
	root := t.TempDir()
	current := filepath.Join(root, "openvibely-desktop")
	staged := LocalStagedUpdate{InstallPath: current, ArtifactPath: current + ".openvibely-new", BackupPath: current + ".openvibely-backup", Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "desktop-operation-1"}
	for path, contents := range map[string]string{staged.InstallPath: "old", staged.ArtifactPath: "new", staged.BackupPath: "old"} {
		if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeBinaryHelperPhase(staged, binaryOutcomeAuthorized); err != nil {
		t.Fatal(err)
	}
	return staged
}

func TestDesktopHelperValidatesHealthAndRollsBackFailedSuccessor(t *testing.T) {
	staged := desktopHelperFixture(t)
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ready":true,"version":"wrong"}`))
	}))
	defer health.Close()
	var starts, stops atomic.Int32
	err := RunDesktopHelper(context.Background(), DesktopHelperConfig{
		ParentPID: 99999999, Current: staged.InstallPath, Staged: staged.ArtifactPath, Backup: staged.BackupPath,
		HealthURL: health.URL, ExpectedVersion: staged.Version, PreviousVersion: staged.PreviousVersion, OutcomeID: staged.OutcomeID,
		WaitTimeout: 100 * time.Millisecond, ValidationTimeout: 30 * time.Millisecond,
		StartCommand: func() (func(context.Context) error, error) {
			starts.Add(1)
			return func(context.Context) error { stops.Add(1); return nil }, nil
		},
	})
	if err == nil {
		t.Fatal("desktop helper accepted the wrong health version")
	}
	if starts.Load() != 2 || stops.Load() != 1 {
		t.Fatalf("starts=%d stops=%d, want rollback relaunch after one failed-successor stop", starts.Load(), stops.Load())
	}
	if data, readErr := os.ReadFile(staged.InstallPath); readErr != nil || string(data) != "old" {
		t.Fatalf("rolled-back desktop = %q, err=%v", data, readErr)
	}
	outcome, readErr := readBinaryHelperOutcome(staged)
	if readErr != nil || outcome.State != binaryOutcomeRolledBack {
		t.Fatalf("outcome=%#v err=%v", outcome, readErr)
	}
}

func TestDesktopHelperRecoveryDoesNotRepeatAmbiguousCompletedExchange(t *testing.T) {
	staged := desktopHelperFixture(t)
	if err := atomicExchangeInstallUnits(staged.InstallPath, staged.ArtifactPath); err != nil {
		t.Fatal(err)
	}
	if err := writeBinaryHelperPhase(staged, binaryOutcomeParentExited); err != nil {
		t.Fatal(err)
	}
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ready":true,"version":"0.6.0"}`))
	}))
	defer health.Close()
	if err := RunDesktopHelper(context.Background(), DesktopHelperConfig{
		ParentPID: 99999999, Current: staged.InstallPath, Staged: staged.ArtifactPath, Backup: staged.BackupPath,
		HealthURL: health.URL, ExpectedVersion: staged.Version, PreviousVersion: staged.PreviousVersion, OutcomeID: staged.OutcomeID,
		Recovery: true, RunningVersion: staged.Version, WaitTimeout: 100 * time.Millisecond, ValidationTimeout: time.Second,
		StartCommand: func() (func(context.Context) error, error) { return func(context.Context) error { return nil }, nil },
	}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(staged.InstallPath); err != nil || string(data) != "new" {
		t.Fatalf("recovery repeated completed exchange: current=%q err=%v", data, err)
	}
}

func TestDesktopHelperResumesJournaledPublishedTargetAndValidates(t *testing.T) {
	staged := desktopHelperFixture(t)
	if err := atomicExchangeInstallUnits(staged.InstallPath, staged.ArtifactPath); err != nil {
		t.Fatal(err)
	}
	if err := writeBinaryHelperPhase(staged, binaryOutcomeTargetPublished); err != nil {
		t.Fatal(err)
	}
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ready":true,"version":"0.6.0"}`))
	}))
	defer health.Close()
	var starts atomic.Int32
	if err := RunDesktopHelper(context.Background(), DesktopHelperConfig{
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
	outcome, err := readBinaryHelperOutcome(staged)
	if err != nil || outcome.State != binaryOutcomeSucceeded {
		t.Fatalf("outcome=%#v err=%v", outcome, err)
	}
}

func TestDesktopHelperArgumentAndRelaunchParsingContracts(t *testing.T) {
	cfg, err := ParseDesktopHelperArgs([]string{
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
		t.Fatalf("ParseDesktopHelperArgs: %v", err)
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
		if _, err := ParseDesktopHelperArgs(args); err == nil {
			t.Fatalf("ParseDesktopHelperArgs(%v) unexpectedly succeeded", args)
		}
	}

	var relaunch DesktopHelperConfig
	metadata := `{"arguments":["OpenVibely","--flag"],"working_directory":"/tmp","executable_relative":"Contents/MacOS/OpenVibely"}`
	if err := LoadDesktopHelperRelaunch(strings.NewReader(metadata), &relaunch); err != nil {
		t.Fatalf("LoadDesktopHelperRelaunch: %v", err)
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
		if err := LoadDesktopHelperRelaunch(strings.NewReader(input), &DesktopHelperConfig{}); err == nil {
			t.Fatalf("LoadDesktopHelperRelaunch(%q) unexpectedly succeeded", input)
		}
	}
	if err := LoadDesktopHelperRelaunch(nil, &DesktopHelperConfig{}); err == nil {
		t.Fatal("nil relaunch reader unexpectedly succeeded")
	}
	if err := LoadDesktopHelperRelaunch(strings.NewReader(metadata), nil); err == nil {
		t.Fatal("nil relaunch config unexpectedly succeeded")
	}
}

func TestRunDesktopHelperEarlyValidationErrors(t *testing.T) {
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
	for _, cfg := range []DesktopHelperConfig{
		{ParentPID: 999999, Current: "relative", Staged: staged, Backup: backup, HealthURL: "http://127.0.0.1:1/health", ExpectedVersion: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "outcome"},
		{ParentPID: os.Getpid(), Current: current, Staged: staged, Backup: backup, HealthURL: "http://127.0.0.1:1/health", ExpectedVersion: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "outcome"},
		{ParentPID: 999999, Current: current, Staged: staged, Backup: backup, HealthURL: "", ExpectedVersion: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "outcome"},
	} {
		if err := RunDesktopHelper(context.Background(), cfg); err == nil {
			t.Fatalf("RunDesktopHelper(%#v) unexpectedly succeeded", cfg)
		}
	}
}
