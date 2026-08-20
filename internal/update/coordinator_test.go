package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/buildinfo"
)

func mockCheckServiceURL(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/updates/check" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":1,"update_available":false}`))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func TestCoordinatorDesktopRestartRequiresJournaledHealthOutcome(t *testing.T) {
	root := t.TempDir()
	drainPath := filepath.Join(root, "drain.json")
	coordinatorPath := filepath.Join(root, "coordinator.json")
	drain := NewDrainManager(nil, nil, 0, nil)
	if err := drain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil || !drain.TakeOwnership(status.Generation) {
		t.Fatalf("own drain: status=%#v err=%v", status, err)
	}
	current := filepath.Join(root, "openvibely-desktop")
	staged := LocalStagedUpdate{InstallPath: current, ArtifactPath: current + ".openvibely-new", BackupPath: current + ".openvibely-backup", Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "desktop-operation-1"}
	for path, contents := range map[string]string{staged.InstallPath: "new", staged.ArtifactPath: "old", staged.BackupPath: "old"} {
		if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeBinaryHelperPhase(staged, binaryOutcomeValidating); err != nil {
		t.Fatal(err)
	}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json")})
	old := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.6.0"}, Distribution: buildinfo.DistributionDesktop}, "stable", drain, nil, false, "", nil)
	if err := old.SetPersistence(coordinatorPath); err != nil {
		t.Fatal(err)
	}
	old.mu.Lock()
	old.state, old.operationGeneration, old.staged = StateRestarting, status.Generation, staged
	old.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: time.Now().Add(time.Hour)}}
	if err := old.persistLocked(); err != nil {
		old.mu.Unlock()
		t.Fatal(err)
	}
	old.mu.Unlock()

	restoredDrain := NewDrainManager(nil, nil, 0, nil)
	if err := restoredDrain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	installer := &fakeBinaryRestartRecoveryInstaller{}
	restarted := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.6.0"}, Distribution: buildinfo.DistributionDesktop}, "stable", restoredDrain, installer, false, "", nil)
	if err := restarted.SetPersistence(coordinatorPath); err != nil {
		t.Fatal(err)
	}
	if snapshot := restarted.Snapshot(); snapshot.State != StateRestarting || snapshot.Drain.State == DrainStateIdle {
		t.Fatalf("desktop restart settled before health outcome: %#v", snapshot)
	}
	restarted.recoveryRetryInterval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restarted.StartRecovery(ctx)
	deadline := time.Now().Add(time.Second)
	for installer.recoveries.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if installer.recoveries.Load() != 1 {
		t.Fatalf("desktop health recovery handoffs=%d", installer.recoveries.Load())
	}
	if snapshot := restarted.Snapshot(); snapshot.State != StateRestarting || snapshot.Drain.State == DrainStateIdle {
		t.Fatalf("desktop drain released without journaled health success: %#v", snapshot)
	}
}

func TestCoordinatorHiddenPackagedOfferStillChecksMetricsWithoutStaging(t *testing.T) {
	var checks atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		checks.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":1,"update_available":false}`))
	}))
	defer server.Close()

	installer := &countingInstaller{}
	client := NewClient(ClientConfig{
		ServiceURL: server.URL,
		Channel:    "stable",
		StatePath:  filepath.Join(t.TempDir(), "client.json"),
		HTTPClient: server.Client(),
		Random:     func(time.Duration) time.Duration { return 0 },
	})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0", Commit: "abc", OS: "linux", Arch: "amd64"}, Distribution: buildinfo.DistributionBinary}, "stable", NewDrainManager(nil, nil, 0, nil), installer, false, "", nil)
	coordinator.SetUpdateNotificationsEnabled(false)

	if err := coordinator.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if checks.Load() != 1 {
		t.Fatalf("metric checks=%d want 1", checks.Load())
	}
	if installer.stages.Load() != 0 {
		t.Fatalf("hidden packaged offer staged %d artifacts", installer.stages.Load())
	}
	if coordinator.Visible() || coordinator.Snapshot().Release != nil {
		t.Fatalf("hidden packaged check exposed update: visible=%v snapshot=%#v", coordinator.Visible(), coordinator.Snapshot())
	}
}

func TestCoordinatorDefaultOffHidesIdleNotificationButKeepsActiveRecoveryVisible(t *testing.T) {
	coordinator := NewCoordinator(nil, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", NewDrainManager(nil, nil, 0, nil), nil, false, "", nil)
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0"}}
	coordinator.state = StateAvailable
	coordinator.SetUpdateNotificationsEnabled(false)
	if coordinator.Visible() {
		t.Fatal("disabled packaged checks left an idle cached update notification visible")
	}

	coordinator.accepted = true
	coordinator.state = StateWaitingForIdle
	if !coordinator.Visible() {
		t.Fatal("disabled packaged checks hid an accepted update recovery")
	}
}

func TestCoordinatorRestartRecoveryConfirmsVersionAndReopensDurableDrain(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	root := t.TempDir()
	drainPath := filepath.Join(root, "drain.json")
	coordinatorPath := filepath.Join(root, "coordinator.json")
	current := filepath.Join(root, "openvibely")
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := drain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	if _, err := drain.BeginDrain(DrainRequest{Lease: time.Hour}); err != nil {
		t.Fatal(err)
	}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
	old := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", drain, nil, false, "", nil)
	if err := old.SetPersistence(coordinatorPath); err != nil {
		t.Fatal(err)
	}
	old.mu.Lock()
	old.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	old.staged = LocalStagedUpdate{ArtifactPath: current + ".openvibely-new", InstallPath: current, BackupPath: current + ".openvibely-backup", Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1"}
	old.state = StateRestarting
	if err := old.persistLocked(); err != nil {
		old.mu.Unlock()
		t.Fatal(err)
	}
	old.mu.Unlock()
	if err := writeBinaryHelperOutcome(old.staged.(LocalStagedUpdate), binaryOutcomeSucceeded); err != nil {
		t.Fatal(err)
	}

	restoredDrain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := restoredDrain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	restarted := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.6.0"}, Distribution: buildinfo.DistributionBinary}, "stable", restoredDrain, nil, false, "", nil)
	if err := restarted.SetPersistence(coordinatorPath); err != nil {
		t.Fatal(err)
	}
	restarted.recoveryRetryInterval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restarted.StartRecovery(ctx)
	deadline := time.Now().Add(time.Second)
	for restarted.Snapshot().State == StateRestarting && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if snapshot := restarted.Snapshot(); snapshot.State != StateSucceeded || snapshot.Drain.State != DrainStateIdle {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

type fakeBinaryRestartRecoveryInstaller struct {
	countingInstaller
	recoveries atomic.Int32
}

func (i *fakeBinaryRestartRecoveryInstaller) RequiresRestartValidation() bool { return true }
func (i *fakeBinaryRestartRecoveryInstaller) RecoverBinaryRestart(context.Context, LocalStagedUpdate) error {
	i.recoveries.Add(1)
	return nil
}

func TestCoordinatorDeadHelperRecoveryExcludesHelperRestart(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "openvibely")
	staged := LocalStagedUpdate{ArtifactPath: current + ".openvibely-new", InstallPath: current, BackupPath: current + ".openvibely-backup", Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1"}
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged.ArtifactPath, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeBinaryHelperPhase(staged, binaryOutcomeAuthorized); err != nil {
		t.Fatal(err)
	}

	drain := NewDrainManager(nil, nil, 0, nil)
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil || !drain.TakeOwnership(status.Generation) {
		t.Fatalf("own drain: status=%#v err=%v", status, err)
	}
	coordinator := NewCoordinator(nil, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", drain, &fakeBinaryRestartRecoveryInstaller{}, false, "", nil)
	coordinator.state, coordinator.operationGeneration, coordinator.staged = StateRestarting, status.Generation, staged
	var helperRestartAcquired bool
	coordinator.binaryRecoveryLeaseHook = func() {
		lease, acquired, err := tryAcquireBinaryHelperLease(binaryHelperLeasePath(staged))
		if err != nil {
			t.Fatalf("helper restart lease: %v", err)
		}
		helperRestartAcquired = acquired
		if acquired {
			_ = lease.Close()
		}
	}

	if !coordinator.recoverDeadBinaryHelper(context.Background(), staged, status.Generation, binaryHelperOutcome{State: binaryOutcomeAuthorized}, "0.5.0") {
		t.Fatal("dead helper recovery did not settle")
	}
	if helperRestartAcquired {
		t.Fatal("duplicate helper acquired the lease before terminalization")
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != StateRolledBack || snapshot.Drain.State != DrainStateIdle {
		t.Fatalf("terminal recovery snapshot = %#v", snapshot)
	}
}

func TestCoordinatorDeadHelperRecoveryClaimsOwnershipBeforeLeaseTransfer(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "openvibely")
	staged := LocalStagedUpdate{ArtifactPath: current + ".openvibely-new", InstallPath: current, BackupPath: current + ".openvibely-backup", Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1"}
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged.BackupPath, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeBinaryHelperPhase(staged, binaryOutcomeTargetPublished); err != nil {
		t.Fatal(err)
	}

	drain := NewDrainManager(nil, nil, 0, nil)
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil || !drain.TakeOwnership(status.Generation) {
		t.Fatalf("own drain: status=%#v err=%v", status, err)
	}
	installer := &fakeBinaryRestartRecoveryInstaller{}
	coordinator := NewCoordinator(nil, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", drain, installer, false, "", nil)
	coordinator.state, coordinator.operationGeneration, coordinator.staged = StateRestarting, status.Generation, staged
	coordinator.binaryRecoveryLeaseHook = func() {
		lease, acquired, err := tryAcquireBinaryHelperLease(binaryHelperLeasePath(staged))
		if err != nil {
			t.Fatalf("helper restart lease: %v", err)
		}
		if acquired {
			_ = lease.Close()
			t.Fatal("duplicate helper acquired lease before recovery ownership was durable")
		}
	}

	if !coordinator.recoverDeadBinaryHelper(context.Background(), staged, status.Generation, binaryHelperOutcome{State: binaryOutcomeTargetPublished}, "0.5.0") {
		t.Fatal("dead helper recovery handoff did not start")
	}
	claim, err := readBinaryHelperRecoveryClaim(staged)
	if err != nil || claim.State != binaryOutcomeRecovering {
		t.Fatalf("recovery ownership claim = %#v, err = %v", claim, err)
	}
	if installer.recoveries.Load() != 1 || !drain.Owns(status.Generation) {
		t.Fatalf("recoveries=%d owns=%v", installer.recoveries.Load(), drain.Owns(status.Generation))
	}

	started := false
	if err := RunBinaryHelper(context.Background(), BinaryHelperConfig{
		ParentPID: 99999999, Current: current, Staged: staged.ArtifactPath, Backup: staged.BackupPath,
		HealthURL: "http://127.0.0.1/health", ExpectedVersion: staged.Version, PreviousVersion: staged.PreviousVersion, OutcomeID: staged.OutcomeID,
		WaitTimeout: time.Millisecond, ValidationTimeout: time.Millisecond,
		StartCommand: func(string, string) (func(context.Context) error, error) {
			started = true
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("duplicate original helper did not exit cleanly: %v", err)
	}
	if started {
		t.Fatal("duplicate original helper acted after recovery ownership transfer")
	}
}

func TestCoordinatorDoesNotRecoverLiveAuthorizedBinaryHelper(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "openvibely")
	staged := LocalStagedUpdate{ArtifactPath: current + ".openvibely-new", InstallPath: current, BackupPath: current + ".openvibely-backup", Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1"}
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged.ArtifactPath, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := marshalBinaryHelperOutcome(staged, binaryOutcomeAuthorized)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteState(binaryHelperAuthorizedPath(current), data); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := tryAcquireBinaryHelperLease(binaryHelperLeasePath(staged))
	if err != nil || !acquired {
		t.Fatalf("acquire live helper lease: acquired=%v err=%v", acquired, err)
	}

	drain := NewDrainManager(nil, nil, 0, nil)
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil || !drain.TakeOwnership(status.Generation) {
		t.Fatalf("own drain: status=%#v err=%v", status, err)
	}
	installer := &fakeBinaryRestartRecoveryInstaller{}
	coordinator := NewCoordinator(nil, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", drain, installer, false, "", nil)
	coordinator.state, coordinator.operationGeneration, coordinator.staged = StateRestarting, status.Generation, staged
	coordinator.recoveryRetryInterval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	coordinator.StartRecovery(ctx)
	time.Sleep(25 * time.Millisecond)
	if snapshot := coordinator.Snapshot(); snapshot.State != StateRestarting || snapshot.Drain.State == DrainStateIdle || installer.recoveries.Load() != 0 {
		t.Fatalf("live helper recovery snapshot=%#v recoveries=%d", snapshot, installer.recoveries.Load())
	}
	cancel()
	time.Sleep(5 * time.Millisecond)
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorCheckDoesNotOverwriteActiveTransition(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	client := NewClient(ClientConfig{ServiceURL: mockCheckServiceURL(t), Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json"), Now: func() time.Time { return now }})
	if err := client.saveState(persistedClientState{LastSuccessfulCheck: now, Cached: &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}}); err != nil {
		t.Fatal(err)
	}
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", NewDrainManager(nil, nil, 0, nil), nil, false, "", nil)
	coordinator.state = StateApplying
	if err := coordinator.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := coordinator.Snapshot().State; got != StateApplying {
		t.Fatalf("active state overwritten by check: %s", got)
	}
}

func TestCoordinatorRejectsConcurrentApplyAndFailedRemoteCancelKeepsDrain(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json"), Now: func() time.Time { return now }})
	installer := &cancelFailInstaller{cancelErr: errors.New("replacement already started")}
	drain := NewDrainManager(nil, nil, time.Hour, func() time.Time { return now })
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionDocker}, "stable", drain, installer, false, "", nil)
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.staged = *coordinator.release
	coordinator.state = StateAvailable
	if err := coordinator.Apply(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Apply(context.Background(), time.Hour); err == nil {
		t.Fatal("concurrent apply accepted")
	}
	coordinator.mu.Lock()
	coordinator.state = StateApplying
	coordinator.mu.Unlock()
	if err := coordinator.Cancel(); err == nil {
		t.Fatal("failed remote cancellation was ignored")
	}
	if drain.Status().State == DrainStateIdle {
		t.Fatal("failed remote cancellation reopened admission")
	}
}

type countingInstaller struct {
	stages, applies atomic.Int32
	stageErr        error
}

type cancelFailInstaller struct {
	countingInstaller
	cancelErr error
}

func (i *cancelFailInstaller) Cancel(context.Context) error { return i.cancelErr }

func TestCoordinatorBinaryAutoRestartDoesNotTreatPendingHelperAsRollback(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	root := t.TempDir()
	drainPath := filepath.Join(root, "drain.json")
	coordinatorPath := filepath.Join(root, "coordinator.json")
	current := filepath.Join(root, "openvibely")
	outcomePath := current + ".openvibely-outcome.json"

	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := drain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !drain.TakeOwnership(status.Generation) {
		t.Fatal("take drain ownership")
	}
	coordinatorState, err := json.Marshal(map[string]any{
		"state": "restarting",
		"release": map[string]any{"metadata": map[string]any{
			"version": "0.6.0", "channel": "stable", "expires_at": "2099-01-01T00:00:00Z",
		}},
		"staged_local": LocalStagedUpdate{
			ArtifactPath: current + ".openvibely-new", InstallPath: current, BackupPath: current + ".openvibely-backup",
			Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1",
		},
		"operation_generation": status.Generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(coordinatorPath, coordinatorState, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outcomePath, []byte(`{"id":"operation-1","state":"pending","previous_version":"0.5.0","desired_version":"0.6.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	restoredDrain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := restoredDrain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	restarted := NewCoordinator(NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }}), CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", restoredDrain, nil, false, "", nil)
	if err := restarted.SetPersistence(coordinatorPath); err != nil {
		t.Fatal(err)
	}
	if snapshot := restarted.Snapshot(); snapshot.State != StateRestarting || snapshot.Drain.State == DrainStateIdle || !restoredDrain.Owns(status.Generation) {
		t.Fatalf("pending helper outcome released admission: snapshot=%#v owns=%v", snapshot, restoredDrain.Owns(status.Generation))
	}
	restarted.recoveryRetryInterval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restarted.StartRecovery(ctx)
	time.Sleep(20 * time.Millisecond)
	if snapshot := restarted.Snapshot(); snapshot.State != StateRestarting || snapshot.Drain.State == DrainStateIdle {
		t.Fatalf("pending helper outcome settled during active replacement: %#v", snapshot)
	}
	staged := LocalStagedUpdate{InstallPath: current, Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1"}
	cancelled, err := cancelBinaryHelperHandoff(staged)
	if err != nil || !cancelled {
		t.Fatalf("cancel pending helper handoff: cancelled=%v err=%v", cancelled, err)
	}
	deadline := time.Now().Add(time.Second)
	for restarted.Snapshot().State == StateRestarting && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if snapshot := restarted.Snapshot(); snapshot.State != StateRolledBack || snapshot.Drain.State != DrainStateIdle {
		t.Fatalf("terminal helper rollback was not reconciled: %#v", snapshot)
	}
}

func TestCoordinatorBinaryPreparedHandoffCrashReleasesExactGeneration(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	root := t.TempDir()
	drainPath := filepath.Join(root, "drain.json")
	coordinatorPath := filepath.Join(root, "coordinator.json")
	current := filepath.Join(root, "openvibely")

	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := drain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !drain.TakeOwnership(status.Generation) {
		t.Fatal("take drain ownership")
	}
	coordinatorState, err := json.Marshal(map[string]any{
		"state": "restarting",
		"release": map[string]any{"metadata": map[string]any{
			"version": "0.6.0", "channel": "stable", "expires_at": "2099-01-01T00:00:00Z",
		}},
		"staged_local": LocalStagedUpdate{
			ArtifactPath: current + ".openvibely-new", InstallPath: current, BackupPath: current + ".openvibely-backup",
			Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1",
		},
		"operation_generation": status.Generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(coordinatorPath, coordinatorState, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryHelperPreparedPath(current), []byte(`{"id":"operation-1","state":"prepared","previous_version":"0.5.0","desired_version":"0.6.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	restoredDrain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := restoredDrain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	restarted := NewCoordinator(NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }}), CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", restoredDrain, nil, false, "", nil)
	if err := restarted.SetPersistence(coordinatorPath); err != nil {
		t.Fatal(err)
	}
	restarted.recoveryRetryInterval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restarted.StartRecovery(ctx)
	deadline := time.Now().Add(time.Second)
	for restarted.Snapshot().State == StateRestarting && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if snapshot := restarted.Snapshot(); snapshot.State != StateRolledBack || snapshot.Drain.State != DrainStateIdle || restoredDrain.Owns(status.Generation) {
		t.Fatalf("prepared handoff crash was not cleaned up: snapshot=%#v owns=%v", snapshot, restoredDrain.Owns(status.Generation))
	}
}

func TestCoordinatorBinaryPreparedClaimRaceKeepsExactGenerationOwned(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "openvibely")
	staged := LocalStagedUpdate{
		ArtifactPath:    current + ".openvibely-new",
		InstallPath:     current,
		BackupPath:      current + ".openvibely-backup",
		Version:         "0.6.0",
		PreviousVersion: "0.5.0",
		OutcomeID:       "operation-1",
	}
	if err := writeBinaryHelperOutcome(staged, binaryOutcomePrepared); err != nil {
		t.Fatal(err)
	}

	drain := NewDrainManager(nil, nil, 0, nil)
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !drain.TakeOwnership(status.Generation) {
		t.Fatal("take drain ownership")
	}
	coordinator := NewCoordinator(nil, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", drain, nil, false, "", nil)
	coordinator.state = StateRestarting
	coordinator.operationGeneration = status.Generation
	coordinator.staged = staged
	coordinator.recoveryRetryInterval = time.Hour
	var claimed atomic.Bool
	coordinator.binaryOutcomeReadHook = func() {
		if claimed.CompareAndSwap(false, true) {
			if err := os.Rename(binaryHelperPreparedPath(current), binaryHelperOutcomePath(current)); err != nil {
				t.Errorf("claim prepared outcome: %v", err)
			}
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	coordinator.StartRecovery(ctx)
	deadline := time.Now().Add(time.Second)
	for !claimed.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !claimed.Load() {
		t.Fatal("helper claim interleaving was not exercised")
	}
	time.Sleep(20 * time.Millisecond)
	if snapshot := coordinator.Snapshot(); snapshot.State != StateRestarting || snapshot.Drain.State == DrainStateIdle || !drain.Owns(status.Generation) {
		t.Fatalf("helper claim race released exact generation: snapshot=%#v owns=%v", snapshot, drain.Owns(status.Generation))
	}
	coordinator.mu.Lock()
	coordinator.operationGeneration = ""
	coordinator.mu.Unlock()
	cancel()
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		lease, acquired, err := tryAcquireBinaryHelperLease(binaryHelperLeasePath(staged))
		if err != nil {
			t.Fatalf("wait for recovery lease release: %v", err)
		}
		if acquired {
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("recovery helper lease was not released")
}

func TestCoordinatorBinaryRestartWithoutHelperHandoffReleasesExactPredecessor(t *testing.T) {
	root := t.TempDir()
	drain := NewDrainManager(nil, nil, 0, nil)
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !drain.TakeOwnership(status.Generation) {
		t.Fatal("take drain ownership")
	}
	current := filepath.Join(root, "openvibely")
	coordinator := NewCoordinator(nil, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", drain, nil, false, "", nil)
	coordinator.staged = LocalStagedUpdate{ArtifactPath: current + ".openvibely-new", InstallPath: current, BackupPath: current + ".openvibely-backup", Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1"}
	coordinator.state, coordinator.operationGeneration = StateRestarting, status.Generation
	coordinator.recoveryRetryInterval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	coordinator.StartRecovery(ctx)
	deadline := time.Now().Add(time.Second)
	for coordinator.Snapshot().State == StateRestarting && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != StateRolledBack || snapshot.Drain.State != DrainStateIdle {
		t.Fatalf("no-handoff recovery = %#v", snapshot)
	}
}

func TestCoordinatorBinaryRollbackStartupReopensDurableDrain(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	root := t.TempDir()
	drainPath := filepath.Join(root, "drain.json")
	coordinatorPath := filepath.Join(root, "coordinator.json")
	current := filepath.Join(root, "openvibely")
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := drain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	if _, err := drain.BeginDrain(DrainRequest{Lease: time.Hour}); err != nil {
		t.Fatal(err)
	}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
	old := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", drain, nil, false, "", nil)
	if err := old.SetPersistence(coordinatorPath); err != nil {
		t.Fatal(err)
	}
	old.mu.Lock()
	old.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	old.staged = LocalStagedUpdate{ArtifactPath: current + ".openvibely-new", InstallPath: current, BackupPath: current + ".openvibely-backup", Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1"}
	old.state = StateRestarting
	if err := old.persistLocked(); err != nil {
		old.mu.Unlock()
		t.Fatal(err)
	}
	old.mu.Unlock()
	if err := writeBinaryHelperOutcome(old.staged.(LocalStagedUpdate), binaryOutcomeRolledBack); err != nil {
		t.Fatal(err)
	}

	restoredDrain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := restoredDrain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	restarted := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", restoredDrain, nil, false, "", nil)
	if err := restarted.SetPersistence(coordinatorPath); err != nil {
		t.Fatal(err)
	}
	restarted.recoveryRetryInterval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restarted.StartRecovery(ctx)
	deadline := time.Now().Add(time.Second)
	for restarted.Snapshot().State == StateRestarting && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if snapshot := restarted.Snapshot(); snapshot.State != StateRolledBack || snapshot.Drain.State != DrainStateIdle {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

type restartingCountingInstaller struct {
	countingInstaller
	validates atomic.Int32
}

func (i *restartingCountingInstaller) RequiresRestartValidation() bool { return true }
func (i *restartingCountingInstaller) Validate(context.Context, ReleaseMetadata) error {
	i.validates.Add(1)
	return nil
}

func (i *countingInstaller) Stage(context.Context, VerifiedRelease) (any, error) {
	i.stages.Add(1)
	if i.stageErr != nil {
		return nil, i.stageErr
	}
	return "staged", nil
}
func (i *countingInstaller) Apply(context.Context, any) error                { i.applies.Add(1); return nil }
func (i *countingInstaller) Validate(context.Context, ReleaseMetadata) error { return nil }
func (i *countingInstaller) Rollback(context.Context, any) error             { return nil }

type acceptanceInspectingInstaller struct {
	countingInstaller
	onStage func()
}

type blockingAcceptanceInstaller struct {
	countingInstaller
	stageStarted chan<- context.Context
	finishStage  <-chan struct{}
}

func (i *blockingAcceptanceInstaller) Stage(ctx context.Context, _ VerifiedRelease) (any, error) {
	i.stages.Add(1)
	i.stageStarted <- ctx
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-i.finishStage:
		return "staged", nil
	}
}

type transientAcceptanceInstaller struct {
	countingInstaller
	firstFailed  chan struct{}
	retryAllowed <-chan struct{}
}

func (i *transientAcceptanceInstaller) Stage(ctx context.Context, _ VerifiedRelease) (any, error) {
	if i.stages.Add(1) == 1 {
		close(i.firstFailed)
		return nil, errors.Join(ErrUpdateRetryable, errors.New("temporary download failure"))
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-i.retryAllowed:
		return "staged", nil
	}
}

type definitiveAcceptanceInstaller struct {
	countingInstaller
	firstFailed chan struct{}
	mu          sync.Mutex
	err         error
}

func (i *definitiveAcceptanceInstaller) Stage(context.Context, VerifiedRelease) (any, error) {
	i.stages.Add(1)
	i.mu.Lock()
	err := i.err
	i.mu.Unlock()
	if err != nil {
		select {
		case <-i.firstFailed:
		default:
			close(i.firstFailed)
		}
		return nil, err
	}
	return "staged", nil
}

func (i *definitiveAcceptanceInstaller) setError(err error) {
	i.mu.Lock()
	i.err = err
	i.mu.Unlock()
}

func waitForCoordinatorState(t *testing.T, coordinator *Coordinator, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if coordinator.Snapshot().State == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("coordinator state = %q, want %q", coordinator.Snapshot().State, want)
}

func (i *acceptanceInspectingInstaller) Stage(ctx context.Context, release VerifiedRelease) (any, error) {
	if i.onStage != nil {
		i.onStage()
	}
	return i.countingInstaller.Stage(ctx, release)
}

func TestCoordinatorManualAcceptanceWithoutStagedArtifactReachesReady(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	tracker := NewWorkTracker()
	finishWork, err := tracker.Start(WorkTask)
	if err != nil {
		t.Fatal(err)
	}
	defer finishWork()
	drain := NewDrainManager(tracker.Active, nil, 0, func() time.Time { return now })
	drain.SetWorkTracker(tracker)
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json"), Now: func() time.Time { return now }})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionDocker}, "stable", drain, nil, true, "", nil)
	t.Cleanup(func() { _ = coordinator.Cancel() })
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.state = StateAvailable
	coordinator.recoveryCtx = context.Background()

	if err := coordinator.Accept(context.Background(), 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	waitForCoordinatorState(t, coordinator, StateWaitingForIdle)
	if tracker.Admit() {
		t.Fatal("manual accepted update left admission open while draining")
	}
	finishWork()
	waitForCoordinatorState(t, coordinator, StateReady)
	if snapshot := coordinator.Snapshot(); snapshot.Drain.State != DrainStateReady || tracker.Admit() {
		t.Fatalf("manual accepted update did not keep admission closed while ready: %#v admit=%v", snapshot, tracker.Admit())
	}
}

func TestCoordinatorAcceptedPackagedUpdateWithoutStagedArtifactFails(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json"), Now: func() time.Time { return now }})
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", drain, &countingInstaller{}, false, "", nil)
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.state = StateAvailable

	if err := coordinator.Accept(context.Background(), 10*time.Minute); err == nil {
		t.Fatal("accepted packaged update without staged artifact")
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != StateAvailable || snapshot.Drain.State != DrainStateIdle {
		t.Fatalf("packaged unstaged update changed state: %#v", snapshot)
	}
}

func TestCoordinatorPackagedReplacementIsStagedBeforeApproval(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	root := t.TempDir()
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
	drain := NewDrainManager(nil, nil, time.Hour, func() time.Time { return now })
	installer := &countingInstaller{}
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", drain, installer, false, "", nil)
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.state = StateAvailable
	if err := coordinator.Stage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if installer.stages.Load() != 1 || !coordinator.Snapshot().Staged {
		t.Fatalf("replacement was not staged before approval: stages=%d snapshot=%#v", installer.stages.Load(), coordinator.Snapshot())
	}
	coordinator.recoveryCtx = context.Background()
	if err := coordinator.Accept(context.Background(), 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for coordinator.Snapshot().State != StateWaitingForIdle && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if installer.stages.Load() != 1 || coordinator.Snapshot().State != StateWaitingForIdle {
		t.Fatalf("approval restaged replacement or failed to drain: stages=%d snapshot=%#v", installer.stages.Load(), coordinator.Snapshot())
	}
}

func TestCoordinatorRecoversAcceptedDrainBeforeCoordinatorGenerationPersistence(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	root := t.TempDir()
	drainPath := filepath.Join(root, "drain.json")
	coordinatorPath := filepath.Join(root, "coordinator.json")
	drain := NewDrainManager(nil, nil, time.Hour, func() time.Time { return now })
	if err := drain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	status, err := drain.BeginDrain(DrainRequest{Lease: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	release := VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	data, err := json.Marshal(map[string]any{
		"state":            StateAvailable,
		"release":          release,
		"staged_release":   release,
		"accepted":         true,
		"acceptance_lease": int64(10 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteState(coordinatorPath, data); err != nil {
		t.Fatal(err)
	}

	restoredDrain := NewDrainManager(nil, nil, time.Hour, func() time.Time { return now })
	if err := restoredDrain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	installer := &countingInstaller{}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionDocker}, "stable", restoredDrain, installer, false, "", nil)
	if err := coordinator.SetPersistence(coordinatorPath); err != nil {
		t.Fatal(err)
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != StateWaitingForIdle || snapshot.Drain.Generation != status.Generation || coordinator.operationGeneration != status.Generation {
		t.Fatalf("accepted drain was not rebound to its exact generation: %#v operation=%q", snapshot, coordinator.operationGeneration)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	coordinator.StartRecovery(ctx)
	time.Sleep(20 * time.Millisecond)
	if installer.stages.Load() != 0 || installer.applies.Load() != 0 {
		t.Fatalf("rebound drain replayed staging or applied before readiness: stages=%d applies=%d", installer.stages.Load(), installer.applies.Load())
	}
}

func TestCoordinatorAcceptedDrainHandoffPersistenceFailureRetriesWithoutAnotherAcceptance(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	root := t.TempDir()
	coordinatorPath := filepath.Join(root, "coordinator.json")
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
	drain := NewDrainManager(nil, nil, time.Hour, func() time.Time { return now })
	if err := drain.SetPersistence(filepath.Join(root, "drain.json")); err != nil {
		t.Fatal(err)
	}
	installer := &countingInstaller{}
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionDocker}, "stable", drain, installer, false, "", nil)
	if err := coordinator.SetPersistence(coordinatorPath); err != nil {
		t.Fatal(err)
	}
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.staged = *coordinator.release
	coordinator.state = StateAvailable
	coordinator.recoveryRetryInterval = time.Millisecond
	var waitingWrites atomic.Int32
	var failedGeneration atomic.Pointer[string]
	coordinator.stateWriter = func(path string, data []byte) error {
		var persisted struct {
			State               string `json:"state"`
			OperationGeneration string `json:"operation_generation"`
		}
		if err := json.Unmarshal(data, &persisted); err != nil {
			return err
		}
		if persisted.State == StateWaitingForIdle {
			generation := persisted.OperationGeneration
			if failedGeneration.CompareAndSwap(nil, &generation) {
				waitingWrites.Add(1)
				return errors.New("temporary waiting-for-idle persistence failure")
			}
			waitingWrites.Add(1)
		}
		return atomicWriteState(path, data)
	}
	lifecycleCtx, stopLifecycle := context.WithCancel(context.Background())
	defer stopLifecycle()
	coordinator.recoveryCtx = lifecycleCtx

	if err := coordinator.Accept(context.Background(), 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot := coordinator.Snapshot()
		failed := failedGeneration.Load()
		if waitingWrites.Load() >= 2 && failed != nil && snapshot.State == StateWaitingForIdle && snapshot.Drain.State != DrainStateIdle && snapshot.Drain.Generation != *failed {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if calls := installer.stages.Load(); calls != 0 {
		t.Fatalf("staging calls after drain handoff retry = %d, want 0", calls)
	}
	if writes := waitingWrites.Load(); writes < 2 {
		t.Fatalf("waiting-for-idle writes = %d, want at least 2", writes)
	}
	snapshot := coordinator.Snapshot()
	failed := failedGeneration.Load()
	if failed == nil || snapshot.Drain.State == DrainStateIdle || snapshot.Drain.Generation == *failed {
		t.Fatalf("accepted drain handoff was not retried with a fresh generation: failed=%v snapshot=%#v", failed, snapshot)
	}
	data, err := os.ReadFile(coordinatorPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted struct {
		State               string `json:"state"`
		OperationGeneration string `json:"operation_generation"`
		Accepted            bool   `json:"accepted"`
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.State != StateWaitingForIdle || !persisted.Accepted || persisted.OperationGeneration != snapshot.Drain.Generation {
		t.Fatalf("accepted drain retry was not durable: %#v drain=%#v", persisted, snapshot.Drain)
	}
	if err := coordinator.Cancel(); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorRecoversAcceptedDrainHandoffAfterCleanupReachedIdle(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	root := t.TempDir()
	coordinatorPath := filepath.Join(root, "coordinator.json")
	drainPath := filepath.Join(root, "drain.json")
	drain := NewDrainManager(nil, nil, time.Hour, func() time.Time { return now })
	if err := drain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	release := VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	data, err := json.Marshal(map[string]any{
		"state":                StateWaitingForIdle,
		"release":              release,
		"staged_release":       release,
		"operation_generation": "released-generation",
		"accepted":             true,
		"acceptance_lease":     int64(10 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteState(coordinatorPath, data); err != nil {
		t.Fatal(err)
	}

	restoredDrain := NewDrainManager(nil, nil, time.Hour, func() time.Time { return now })
	if err := restoredDrain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	installer := &countingInstaller{}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionDocker}, "stable", restoredDrain, installer, false, "", nil)
	if err := coordinator.SetPersistence(coordinatorPath); err != nil {
		t.Fatal(err)
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != StateAvailable || !coordinator.accepted || coordinator.operationGeneration != "" {
		t.Fatalf("accepted handoff cleanup was not normalized for retry: %#v accepted=%t operation=%q", snapshot, coordinator.accepted, coordinator.operationGeneration)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	coordinator.StartRecovery(ctx)
	waitForCoordinatorState(t, coordinator, StateWaitingForIdle)
	if installer.stages.Load() != 0 {
		t.Fatalf("recovered staged handoff restaged artifact: %d", installer.stages.Load())
	}
	if err := coordinator.Cancel(); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorRestartInstallerDefersValidationUntilNewProcess(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json"), Now: func() time.Time { return now }})
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	installer := &restartingCountingInstaller{}
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", drain, installer, false, "", nil)
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.staged = "staged"
	coordinator.state = StateAvailable

	if err := coordinator.Apply(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for coordinator.Snapshot().State != StateRestarting && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != StateRestarting {
		t.Fatalf("state = %s", snapshot.State)
	}
	if installer.applies.Load() != 1 || installer.validates.Load() != 0 {
		t.Fatalf("apply=%d validate=%d", installer.applies.Load(), installer.validates.Load())
	}
	if drain.Status().State == DrainStateIdle {
		t.Fatal("restart validation reopened admission before the new process validated its version")
	}
}

func TestCoordinatorResumesPersistedWaitingDrainBeforeCreatingReplacement(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !drain.TakeOwnership(status.Generation) {
		t.Fatal("simulate crash after drain ownership")
	}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json"), Now: func() time.Time { return now }})
	installer := &countingInstaller{}
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionDocker}, "stable", drain, installer, false, "", nil)
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.staged = *coordinator.release
	coordinator.state, coordinator.operationGeneration = StateWaitingForIdle, status.Generation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	coordinator.StartRecovery(ctx)
	deadline := time.Now().Add(time.Second)
	var snapshot CoordinatorSnapshot
	for time.Now().Before(deadline) {
		snapshot = coordinator.Snapshot()
		if installer.applies.Load() == 1 && snapshot.State == StateSucceeded {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if installer.applies.Load() != 1 || snapshot.State != StateSucceeded {
		t.Fatalf("applies=%d snapshot=%#v", installer.applies.Load(), snapshot)
	}
}

type resumeInstaller struct {
	countingInstaller
	resumed chan struct{}
}

func (i *resumeInstaller) Resume(context.Context) error { close(i.resumed); return nil }

func TestCoordinatorResumesPersistedInstallerRequestAfterRestart(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	root := t.TempDir()
	drainPath, coordinatorPath := filepath.Join(root, "drain.json"), filepath.Join(root, "coordinator.json")
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := drain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	if _, err := drain.BeginDrain(DrainRequest{Lease: time.Hour}); err != nil {
		t.Fatal(err)
	}
	generation := drain.Status().Generation
	if !drain.TakeOwnership(generation) {
		t.Fatal("take drain ownership")
	}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
	old := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionDocker}, "stable", drain, nil, false, "", nil)
	if err := old.SetPersistence(coordinatorPath); err != nil {
		t.Fatal(err)
	}
	old.mu.Lock()
	old.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	old.staged = *old.release
	old.state, old.operationGeneration = StateApplying, generation
	old.persistLocked()
	old.mu.Unlock()

	restoredDrain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := restoredDrain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	installer := &resumeInstaller{resumed: make(chan struct{})}
	restarted := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionDocker}, "stable", restoredDrain, installer, false, "", nil)
	if err := restarted.SetPersistence(coordinatorPath); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restarted.StartRecovery(ctx)
	select {
	case <-installer.resumed:
	case <-time.After(time.Second):
		t.Fatal("persisted installer request was not resumed")
	}
	deadline := time.Now().Add(time.Second)
	for restarted.Snapshot().State != StateSucceeded && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if snapshot := restarted.Snapshot(); snapshot.State != StateSucceeded || snapshot.Drain.State != DrainStateIdle {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

func TestCoordinatorHostedStageRejectsWithoutChangingState(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json"), Now: func() time.Time { return now }})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionHosted}, "stable", NewDrainManager(nil, nil, 0, nil), nil, false, "", nil)
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.state = StateAvailable

	if err := coordinator.Stage(context.Background()); err == nil {
		t.Fatal("Hosted stage accepted without a local installer")
	}
	if got := coordinator.Snapshot().State; got != StateAvailable {
		t.Fatalf("Hosted stage changed state to %q", got)
	}
}

type cancelledResumeInstaller struct{ countingInstaller }

func (*cancelledResumeInstaller) Resume(context.Context) error { return ErrUpdateCancelled }

func TestCoordinatorRestartAfterAcceptedDockerCancellationReopensDrain(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !drain.TakeOwnership(status.Generation) {
		t.Fatal("take Docker drain ownership")
	}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json"), Now: func() time.Time { return now }})
	installer := &cancelledResumeInstaller{}
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionDocker}, "stable", drain, installer, false, "", nil)
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.staged = *coordinator.release
	coordinator.state, coordinator.operationGeneration = StateApplying, status.Generation

	coordinator.StartRecovery(context.Background())
	deadline := time.Now().Add(time.Second)
	for coordinator.Snapshot().State != StateIdle && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != StateIdle || snapshot.Drain.State != DrainStateIdle {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

func TestCoordinatorManagedLifecycleOverridesHostedAPIState(t *testing.T) {
	coordinator := NewCoordinator(nil, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionHosted}, "stable", NewDrainManager(nil, nil, 0, nil), nil, false, "", nil)
	coordinator.SetManagedStateProvider(func() ManagedUpdateState {
		return ManagedUpdateState{Active: true, State: StateWaitingForIdle, DesiredVersion: "0.6.0", ReleaseNotesURL: "https://example.invalid/release"}
	})
	snapshot := coordinator.Snapshot()
	if snapshot.State != StateWaitingForIdle || snapshot.Release == nil || snapshot.Release.Metadata.Version != "0.6.0" || snapshot.Release.Metadata.ReleaseNotesURL != "https://example.invalid/release" {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

type deferredRecoveryInstaller struct {
	countingInstaller
	ready atomic.Bool
}

func (i *deferredRecoveryInstaller) RecoveryReady() bool { return i.ready.Load() }

func TestCoordinatorManualRecoveryDoesNotRequireInstaller(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json"), Now: func() time.Time { return now }})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionDocker}, "stable", drain, nil, true, "", nil)
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.state, coordinator.operationGeneration = StateWaitingForIdle, status.Generation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	coordinator.StartRecovery(ctx)
	deadline := time.Now().Add(time.Second)
	for coordinator.Snapshot().State != StateReady && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != StateReady || snapshot.Drain.State == DrainStateIdle {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

func TestCoordinatorRecoveryWaitsUntilInstallerIsBound(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json"), Now: func() time.Time { return now }})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionDesktop}, "stable", drain, nil, false, "", nil)
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.staged = "staged"
	coordinator.state, coordinator.operationGeneration = StateWaitingForIdle, status.Generation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	coordinator.StartRecovery(ctx)

	installer := &countingInstaller{}
	coordinator.mu.Lock()
	coordinator.installer = installer
	coordinator.mu.Unlock()
	coordinator.StartRecovery(nil)
	deadline := time.Now().Add(time.Second)
	for installer.applies.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if installer.applies.Load() != 1 {
		t.Fatal("recovery did not start after desktop installer binding")
	}
}

func TestCoordinatorRecoveryWaitsUntilInstallerIsReady(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json"), Now: func() time.Time { return now }})
	installer := &deferredRecoveryInstaller{}
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", drain, installer, false, "", nil)
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.staged = "staged"
	coordinator.state, coordinator.operationGeneration = StateWaitingForIdle, status.Generation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	coordinator.StartRecovery(ctx)
	time.Sleep(300 * time.Millisecond)
	if installer.applies.Load() != 0 || drain.Status().State == DrainStateIdle {
		t.Fatalf("recovery ran before installer readiness: applies=%d drain=%#v", installer.applies.Load(), drain.Status())
	}
	installer.ready.Store(true)
	coordinator.StartRecovery(ctx)
	deadline := time.Now().Add(time.Second)
	for installer.applies.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if installer.applies.Load() != 1 {
		t.Fatal("recovery did not start after installer became ready")
	}
}

type recoveryPendingInstaller struct {
	countingInstaller
	rollbacks atomic.Int32
}

func (i *recoveryPendingInstaller) Apply(context.Context, any) error {
	i.applies.Add(1)
	return ErrUpdateRecoveryPending
}

func (i *recoveryPendingInstaller) Rollback(context.Context, any) error {
	i.rollbacks.Add(1)
	return nil
}

type retryingRecoveryInstaller struct {
	countingInstaller
	resumes   atomic.Int32
	rollbacks atomic.Int32
}

func (i *retryingRecoveryInstaller) Apply(context.Context, any) error {
	i.applies.Add(1)
	return ErrUpdateRecoveryPending
}

func (i *retryingRecoveryInstaller) Resume(context.Context) error {
	if i.resumes.Add(1) < 3 {
		return ErrUpdateRecoveryPending
	}
	return nil
}

func (i *retryingRecoveryInstaller) Rollback(context.Context, any) error {
	i.rollbacks.Add(1)
	return nil
}

func TestCoordinatorRetriesAmbiguousInstallerRequestWithoutReopeningDrain(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	installer := &retryingRecoveryInstaller{}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json"), Now: func() time.Time { return now }})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, "stable", drain, installer, false, "", nil)
	coordinator.recoveryRetryInterval = time.Millisecond
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.staged = "staged"
	coordinator.state = StateAvailable
	if err := coordinator.Apply(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	ownedWhilePending := false
	for coordinator.Snapshot().State != StateSucceeded && time.Now().Before(deadline) {
		snapshot := coordinator.Snapshot()
		if snapshot.State == StateApplying && drain.Owns(snapshot.Drain.Generation) {
			ownedWhilePending = true
		}
		time.Sleep(time.Millisecond)
	}
	snapshot := coordinator.Snapshot()
	if !ownedWhilePending || snapshot.State != StateSucceeded || snapshot.Drain.State != DrainStateIdle {
		t.Fatalf("ownedWhilePending=%v snapshot=%#v", ownedWhilePending, snapshot)
	}
	if installer.applies.Load() != 1 || installer.resumes.Load() != 3 || installer.rollbacks.Load() != 0 {
		t.Fatalf("apply=%d resume=%d rollback=%d", installer.applies.Load(), installer.resumes.Load(), installer.rollbacks.Load())
	}
}

func TestCoordinatorRevalidatesMetadataAfterDrainBeforeInstall(t *testing.T) {
	for _, distribution := range []string{buildinfo.DistributionDesktop, buildinfo.DistributionBinary} {
		t.Run(distribution, func(t *testing.T) {
			var unix atomic.Int64
			unix.Store(1000)
			now := func() time.Time { return time.Unix(unix.Load(), 0).UTC() }
			client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json"), Now: now})
			drain := NewDrainManager(nil, nil, 0, now)
			installer := &countingInstaller{}
			coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: distribution}, "stable", drain, installer, false, "", nil)
			coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now().Add(time.Second)}}
			coordinator.staged = "staged"
			coordinator.state = StateAvailable
			if err := coordinator.Apply(context.Background(), time.Hour); err != nil {
				t.Fatal(err)
			}
			unix.Add(2)
			deadline := time.Now().Add(2 * time.Second)
			for coordinator.Snapshot().State == StateWaitingForIdle && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if installer.applies.Load() != 0 {
				t.Fatal("installer ran after signed metadata expired during drain")
			}
			snapshot := coordinator.Snapshot()
			if snapshot.State != StateFailed || snapshot.Drain.State != DrainStateIdle {
				t.Fatalf("snapshot=%#v", snapshot)
			}
		})
	}
}

func TestCoordinatorManualLeaseExpiryAutonomouslyClearsReadyOperation(t *testing.T) {
	var unix atomic.Int64
	unix.Store(1000)
	now := func() time.Time { return time.Unix(unix.Load(), 0).UTC() }
	root := t.TempDir()
	drain := NewDrainManager(nil, nil, 0, now)
	if err := drain.SetPersistence(filepath.Join(root, "drain.json")); err != nil {
		t.Fatal(err)
	}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: now})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionDocker}, "stable", drain, nil, true, "", nil)
	if err := coordinator.SetPersistence(filepath.Join(root, "coordinator.json")); err != nil {
		t.Fatal(err)
	}
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now().Add(time.Hour)}}
	coordinator.state = StateAvailable
	if err := coordinator.Apply(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for coordinator.Snapshot().State != StateReady && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if coordinator.Snapshot().State != StateReady {
		t.Fatalf("manual update never became ready: %#v", coordinator.Snapshot())
	}
	unix.Add(2)
	deadline = time.Now().Add(2 * time.Second)
	for coordinator.Snapshot().State != StateIdle && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != StateIdle || snapshot.Drain.State != DrainStateIdle || coordinator.operationGeneration != "" {
		t.Fatalf("expired manual operation was stranded: %#v generation=%q", snapshot, coordinator.operationGeneration)
	}
	restored := NewCoordinator(client, coordinator.current, "stable", drain, nil, true, "", nil)
	if err := restored.SetPersistence(filepath.Join(root, "coordinator.json")); err != nil {
		t.Fatal(err)
	}
	if snapshot := restored.Snapshot(); snapshot.State != StateIdle {
		t.Fatalf("durable coordinator state was not cleared: %#v", snapshot)
	}
}

func TestCoordinatorManualReadyRestartResumesLeaseExpiryReconciliation(t *testing.T) {
	var unix atomic.Int64
	unix.Store(1000)
	now := func() time.Time { return time.Unix(unix.Load(), 0).UTC() }
	root := t.TempDir()
	drainPath := filepath.Join(root, "drain.json")
	coordinatorPath := filepath.Join(root, "coordinator.json")
	drain := NewDrainManager(nil, nil, 0, now)
	if err := drain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: now})
	old := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionDocker}, "stable", drain, nil, true, "", nil)
	if err := old.SetPersistence(coordinatorPath); err != nil {
		t.Fatal(err)
	}
	old.mu.Lock()
	old.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now().Add(time.Hour)}}
	old.state, old.operationGeneration = StateReady, status.Generation
	if err := old.persistLocked(); err != nil {
		old.mu.Unlock()
		t.Fatal(err)
	}
	old.mu.Unlock()

	restoredDrain := NewDrainManager(nil, nil, 0, now)
	if err := restoredDrain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	restarted := NewCoordinator(client, old.current, "stable", restoredDrain, nil, true, "", nil)
	if err := restarted.SetPersistence(coordinatorPath); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restarted.StartRecovery(ctx)
	unix.Add(2)
	deadline := time.Now().Add(2 * time.Second)
	for restarted.Snapshot().State != StateIdle && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if snapshot := restarted.Snapshot(); snapshot.State != StateIdle || snapshot.Drain.State != DrainStateIdle {
		t.Fatalf("restarted manual ready operation was stranded: %#v", snapshot)
	}
}

func TestCoordinatorPreservesApplyingStateForDurableInstallerReplay(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	installer := &recoveryPendingInstaller{}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json"), Now: func() time.Time { return now }})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}}, "stable", drain, installer, false, "", nil)
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.staged = "staged"
	coordinator.state = StateAvailable
	if err := coordinator.Apply(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for installer.applies.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	snapshot := coordinator.Snapshot()
	if installer.applies.Load() != 1 || installer.rollbacks.Load() != 0 || snapshot.State != StateApplying || !drain.Owns(snapshot.Drain.Generation) {
		t.Fatalf("applies=%d rollbacks=%d snapshot=%#v", installer.applies.Load(), installer.rollbacks.Load(), snapshot)
	}
}

func TestCoordinatorRestartAutonomouslyExpiresOrphanDrainAfterWaitingTransitionCrash(t *testing.T) {
	var unix atomic.Int64
	unix.Store(1000)
	now := func() time.Time { return time.Unix(unix.Load(), 0).UTC() }
	root := t.TempDir()
	drainPath := filepath.Join(root, "drain.json")
	coordinatorPath := filepath.Join(root, "coordinator.json")

	oldDrain := NewDrainManager(nil, nil, 0, now)
	if err := oldDrain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	if _, err := oldDrain.BeginDrain(DrainRequest{Lease: time.Second}); err != nil {
		t.Fatal(err)
	}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: now})
	oldCoordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", oldDrain, nil, false, "", nil)
	if err := oldCoordinator.SetPersistence(coordinatorPath); err != nil {
		t.Fatal(err)
	}
	oldCoordinator.mu.Lock()
	oldCoordinator.state = StateAvailable
	oldCoordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now().Add(time.Hour)}}
	if err := oldCoordinator.persistLocked(); err != nil {
		oldCoordinator.mu.Unlock()
		t.Fatal(err)
	}
	oldCoordinator.mu.Unlock()

	restartedDrain := NewDrainManager(nil, nil, 0, now)
	if err := restartedDrain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	var expiryWrites atomic.Int32
	restartedDrain.stateWriter = func(path string, data []byte) error {
		if bytes.Contains(data, []byte(`"state":"idle"`)) && expiryWrites.Add(1) < 3 {
			return errors.New("transient restart drain storage failure")
		}
		return atomicWriteState(path, data)
	}
	restartedDrain.supervisorInterval = 5 * time.Millisecond
	restarted := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", restartedDrain, nil, false, "", nil)
	if err := restarted.SetPersistence(coordinatorPath); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restartedDrain.StartExpirySupervisor(ctx)
	unix.Add(2)

	select {
	case <-restartedDrain.Reopened():
	case <-time.After(time.Second):
		t.Fatal("orphan persisted drain did not expire autonomously after restart")
	}
	persisted := NewDrainManager(nil, nil, 0, now)
	if err := persisted.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	if !persisted.Admit() {
		t.Fatal("autonomous expiry was not durable")
	}
	if expiryWrites.Load() < 3 {
		t.Fatalf("expiry writes=%d, want transient failures to be retried", expiryWrites.Load())
	}
}

func TestCoordinatorApplyPersistenceFailureDoesNotStartReplacement(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	installer := &countingInstaller{}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json"), Now: func() time.Time { return now }})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", drain, installer, false, "", nil)
	if err := coordinator.SetPersistence(filepath.Join(t.TempDir(), "coordinator.json")); err != nil {
		t.Fatal(err)
	}
	coordinator.stateWriter = func(string, []byte) error { return errors.New("disk full") }
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.staged = "staged"
	coordinator.state = StateAvailable
	if err := coordinator.Apply(context.Background(), time.Hour); err == nil {
		t.Fatal("apply accepted an undurable coordinator transition")
	}
	if installer.applies.Load() != 0 || drain.Status().State != DrainStateIdle {
		t.Fatalf("applies=%d drain=%#v", installer.applies.Load(), drain.Status())
	}
}

func TestCoordinatorApplyPersistenceAndReleaseFailureRetriesCleanupAutonomously(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	root := t.TempDir()
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := drain.SetPersistence(filepath.Join(root, "drain.json")); err != nil {
		t.Fatal(err)
	}
	storageAvailable := atomic.Bool{}
	drain.stateWriter = func(path string, data []byte) error {
		if bytes.Contains(data, []byte(`"state":"idle"`)) && !storageAvailable.Load() {
			return errors.New("drain storage unavailable")
		}
		return atomicWriteState(path, data)
	}
	installer := &countingInstaller{}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", drain, installer, false, "", nil)
	if err := coordinator.SetPersistence(filepath.Join(root, "coordinator.json")); err != nil {
		t.Fatal(err)
	}
	coordinator.stateWriter = func(path string, data []byte) error {
		if bytes.Contains(data, []byte(`"state":"waiting_for_idle"`)) {
			return errors.New("coordinator storage unavailable")
		}
		return atomicWriteState(path, data)
	}
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.staged = "staged"
	coordinator.state = StateAvailable
	if err := coordinator.Apply(context.Background(), time.Hour); err == nil {
		t.Fatal("apply accepted an undurable waiting transition")
	}
	if installer.applies.Load() != 0 {
		t.Fatalf("installer applies=%d, want 0", installer.applies.Load())
	}
	select {
	case <-drain.Reopened():
		t.Fatal("drain reopened while durable storage was unavailable")
	case <-time.After(300 * time.Millisecond):
	}
	storageAvailable.Store(true)
	select {
	case <-drain.Reopened():
	case <-time.After(3 * time.Second):
		t.Fatal("drain cleanup was not retried after storage recovered")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && coordinator.Snapshot().State != StateAvailable {
		time.Sleep(10 * time.Millisecond)
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != StateAvailable || snapshot.Drain.State != DrainStateIdle {
		t.Fatalf("cleanup did not restore retryable state: %#v", snapshot)
	}
}

func TestCoordinatorPostOwnershipPersistenceFailureDurablyReleasesDrain(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	root := t.TempDir()
	drainPath := filepath.Join(root, "drain.json")
	coordinatorPath := filepath.Join(root, "coordinator.json")
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := drain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	var releaseAttempts atomic.Int32
	drain.stateWriter = func(path string, data []byte) error {
		if bytes.Contains(data, []byte(`"state":"idle"`)) && releaseAttempts.Add(1) == 1 {
			return errors.New("transient drain persistence failure")
		}
		return atomicWriteState(path, data)
	}
	installer := &countingInstaller{}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", drain, installer, false, "", nil)
	if err := coordinator.SetPersistence(coordinatorPath); err != nil {
		t.Fatal(err)
	}
	var applyingWrites atomic.Int32
	coordinator.stateWriter = func(path string, data []byte) error {
		if bytes.Contains(data, []byte(`"state":"applying"`)) {
			applyingWrites.Add(1)
			return errors.New("persist applying transition")
		}
		return atomicWriteState(path, data)
	}
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.staged = "staged"
	coordinator.state = StateAvailable
	if err := coordinator.Apply(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := coordinator.Snapshot()
		if snapshot.State == StateFailed && snapshot.Drain.State == DrainStateIdle {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if installer.applies.Load() != 0 {
		t.Fatalf("installer applies = %d, want 0", installer.applies.Load())
	}
	if applyingWrites.Load() == 0 || releaseAttempts.Load() < 2 {
		t.Fatalf("applying writes=%d release attempts=%d", applyingWrites.Load(), releaseAttempts.Load())
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != StateFailed || snapshot.Drain.State != DrainStateIdle {
		t.Fatalf("cleanup did not settle: %#v", snapshot)
	}

	restoredDrain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := restoredDrain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	restored := NewCoordinator(client, coordinator.current, "stable", restoredDrain, installer, false, "", nil)
	if err := restored.SetPersistence(coordinatorPath); err != nil {
		t.Fatal(err)
	}
	if snapshot := restored.Snapshot(); snapshot.State != StateFailed || snapshot.Drain.State != DrainStateIdle {
		t.Fatalf("durable cleanup did not survive restart: %#v", snapshot)
	}
}

func TestCoordinatorPostOwnershipRestartPersistenceFailureDoesNotStartReplacement(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	root := t.TempDir()
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := drain.SetPersistence(filepath.Join(root, "drain.json")); err != nil {
		t.Fatal(err)
	}
	installer := &restartingCountingInstaller{}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", drain, installer, false, "", nil)
	if err := coordinator.SetPersistence(filepath.Join(root, "coordinator.json")); err != nil {
		t.Fatal(err)
	}
	coordinator.stateWriter = func(path string, data []byte) error {
		if bytes.Contains(data, []byte(`"state":"restarting"`)) {
			return errors.New("persist restarting transition")
		}
		return atomicWriteState(path, data)
	}
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.staged = "staged"
	coordinator.state = StateAvailable
	if err := coordinator.Apply(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for coordinator.Snapshot().Drain.State != DrainStateIdle && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if installer.applies.Load() != 0 {
		t.Fatalf("installer applies = %d, want 0", installer.applies.Load())
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != StateFailed || snapshot.Drain.State != DrainStateIdle {
		t.Fatalf("restart cleanup did not settle: %#v", snapshot)
	}
}

func TestCoordinatorCancellationFailureDuringOwnedCleanupRemainsSupervised(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	root := t.TempDir()
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := drain.SetPersistence(filepath.Join(root, "drain.json")); err != nil {
		t.Fatal(err)
	}
	var permitRelease atomic.Bool
	drain.stateWriter = func(path string, data []byte) error {
		if bytes.Contains(data, []byte(`"state":"idle"`)) && !permitRelease.Load() {
			return errors.New("drain storage unavailable")
		}
		return atomicWriteState(path, data)
	}
	installer := &countingInstaller{}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", drain, installer, false, "", nil)
	if err := coordinator.SetPersistence(filepath.Join(root, "coordinator.json")); err != nil {
		t.Fatal(err)
	}
	coordinator.stateWriter = func(path string, data []byte) error {
		if bytes.Contains(data, []byte(`"state":"applying"`)) {
			return errors.New("persist applying transition")
		}
		return atomicWriteState(path, data)
	}
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.staged = "staged"
	coordinator.state = StateAvailable
	if err := coordinator.Apply(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !drain.Owns(coordinator.Snapshot().Drain.Generation) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := coordinator.Cancel(); err == nil {
		t.Fatal("cancellation reported success while durable drain release failed")
	}
	permitRelease.Store(true)
	deadline = time.Now().Add(2 * time.Second)
	for coordinator.Snapshot().Drain.State != DrainStateIdle && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if snapshot := coordinator.Snapshot(); snapshot.Drain.State != DrainStateIdle || snapshot.State != StateFailed {
		t.Fatalf("failed cancellation was not durably supervised: %#v", snapshot)
	}
	if installer.applies.Load() != 0 {
		t.Fatalf("installer applies = %d, want 0", installer.applies.Load())
	}
}

func TestCoordinatorRejectsLocalCancelForHostedManagedOperation(t *testing.T) {
	coordinator := NewCoordinator(nil, CurrentBuild{Distribution: buildinfo.DistributionHosted}, "stable", NewDrainManager(nil, nil, 0, nil), nil, false, "", nil)
	coordinator.SetManagedStateProvider(func() ManagedUpdateState {
		return ManagedUpdateState{Active: true, State: StateWaitingForIdle, DesiredVersion: "0.6.0"}
	})
	if err := coordinator.Cancel(); err == nil {
		t.Fatal("local cancel accepted a Hosted-managed operation")
	}
}

func TestCoordinatorCancelSupervisesTransientDrainReleaseFailure(t *testing.T) {
	drain := NewDrainManager(nil, nil, 0, time.Now)
	if err := drain.SetPersistence(filepath.Join(t.TempDir(), "drain.json")); err != nil {
		t.Fatal(err)
	}
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	var releaseWrites atomic.Int32
	drain.stateWriter = func(path string, data []byte) error {
		if bytes.Contains(data, []byte(`"state":"idle"`)) && releaseWrites.Add(1) == 1 {
			return errors.New("transient cancellation release failure")
		}
		return atomicWriteState(path, data)
	}
	coordinator := NewCoordinator(nil, CurrentBuild{}, "stable", drain, nil, true, "", nil)
	ctx, cancel := context.WithCancel(context.Background())
	coordinator.recoveryCtx = ctx
	t.Cleanup(cancel)
	coordinator.state, coordinator.operationGeneration = StateWaitingForIdle, status.Generation
	if err := coordinator.Cancel(); err == nil {
		t.Fatal("cancel reported success after the first durable drain release failed")
	}
	deadline := time.Now().Add(3 * time.Second)
	for coordinator.Snapshot().State != StateIdle && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != StateIdle || snapshot.Drain.State != DrainStateIdle {
		t.Fatalf("cancel cleanup did not settle: %#v", snapshot)
	}
	if releaseWrites.Load() < 2 {
		t.Fatalf("cancel release writes=%d, want retry", releaseWrites.Load())
	}
}

func TestCoordinatorCancelReportsDurableDrainReleaseFailure(t *testing.T) {
	drain := NewDrainManager(nil, nil, 0, time.Now)
	if err := drain.SetPersistence(filepath.Join(t.TempDir(), "drain.json")); err != nil {
		t.Fatal(err)
	}
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	coordinator := NewCoordinator(nil, CurrentBuild{}, "stable", drain, nil, true, "", nil)
	ctx, cancel := context.WithCancel(context.Background())
	coordinator.recoveryCtx = ctx
	t.Cleanup(cancel)
	coordinator.state, coordinator.operationGeneration = StateWaitingForIdle, status.Generation
	drain.stateWriter = func(string, []byte) error { return errors.New("disk full") }
	if err := coordinator.Cancel(); err == nil {
		t.Fatal("cancel reported success after durable drain release failed")
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != StateWaitingForIdle || snapshot.Drain.State == DrainStateIdle {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

func TestCoordinatorRevalidatesMetadataExpiryBeforeStageAndApply(t *testing.T) {

	now := time.Unix(1000, 0).UTC()
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json"), Now: func() time.Time { return now }})
	current := CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}
	installer := &countingInstaller{}
	coordinator := NewCoordinator(client, current, "stable", NewDrainManager(nil, nil, 0, func() time.Time { return now }), installer, false, "", nil)
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Minute)}}
	coordinator.state = StateAvailable

	now = now.Add(2 * time.Minute)
	if err := coordinator.Stage(context.Background()); err == nil {
		t.Fatal("stage accepted expired metadata")
	}
	if installer.stages.Load() != 0 {
		t.Fatalf("installer stages = %d", installer.stages.Load())
	}
	coordinator.release.Metadata.ExpiresAt = now.Add(time.Minute)
	if err := coordinator.Stage(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if err := coordinator.Apply(context.Background(), time.Minute); err == nil {
		t.Fatal("apply accepted metadata that expired after staging")
	}
	if installer.applies.Load() != 0 || coordinator.drain.Status().State != DrainStateIdle {
		t.Fatalf("apply=%d drain=%#v", installer.applies.Load(), coordinator.drain.Status())
	}
}

type phasePersistenceInstaller struct {
	countingInstaller
	applyErr    error
	rollbackErr error
	validates   atomic.Int32
	rollbacks   atomic.Int32
}

func (i *phasePersistenceInstaller) Apply(context.Context, any) error {
	i.applies.Add(1)
	return i.applyErr
}
func (i *phasePersistenceInstaller) Validate(context.Context, ReleaseMetadata) error {
	i.validates.Add(1)
	return nil
}
func (i *phasePersistenceInstaller) Rollback(context.Context, any) error {
	i.rollbacks.Add(1)
	return i.rollbackErr
}

func TestCoordinatorRetriesValidatingPersistenceAfterInstallerApply(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	root := t.TempDir()
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := drain.SetPersistence(filepath.Join(root, "drain.json")); err != nil {
		t.Fatal(err)
	}
	installer := &phasePersistenceInstaller{}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionDocker}, "stable", drain, installer, false, "", nil)
	if err := coordinator.SetPersistence(filepath.Join(root, "coordinator.json")); err != nil {
		t.Fatal(err)
	}
	var validatingWrites atomic.Int32
	coordinator.stateWriter = func(path string, data []byte) error {
		if bytes.Contains(data, []byte(`"state":"validating"`)) && validatingWrites.Add(1) == 1 {
			return errors.New("transient validating persistence failure")
		}
		return atomicWriteState(path, data)
	}
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.staged = "staged"
	coordinator.state = StateAvailable
	if err := coordinator.Apply(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for coordinator.Snapshot().State != StateSucceeded && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != StateSucceeded || snapshot.Drain.State != DrainStateIdle {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	if validatingWrites.Load() < 2 || installer.applies.Load() != 1 || installer.validates.Load() != 1 {
		t.Fatalf("validating writes=%d applies=%d validates=%d", validatingWrites.Load(), installer.applies.Load(), installer.validates.Load())
	}
}

func TestCoordinatorRetriesRollingBackPersistenceBeforeRollback(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	root := t.TempDir()
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := drain.SetPersistence(filepath.Join(root, "drain.json")); err != nil {
		t.Fatal(err)
	}
	installer := &phasePersistenceInstaller{applyErr: errors.New("apply failed")}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionDocker}, "stable", drain, installer, false, "", nil)
	if err := coordinator.SetPersistence(filepath.Join(root, "coordinator.json")); err != nil {
		t.Fatal(err)
	}
	var rollbackWrites atomic.Int32
	coordinator.stateWriter = func(path string, data []byte) error {
		if bytes.Contains(data, []byte(`"state":"rolling_back"`)) && rollbackWrites.Add(1) == 1 {
			return errors.New("transient rollback persistence failure")
		}
		return atomicWriteState(path, data)
	}
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.staged = "staged"
	coordinator.state = StateAvailable
	if err := coordinator.Apply(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for coordinator.Snapshot().State != StateRolledBack && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != StateRolledBack || snapshot.Drain.State != DrainStateIdle {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	if rollbackWrites.Load() < 2 || installer.rollbacks.Load() != 1 {
		t.Fatalf("rollback writes=%d rollbacks=%d", rollbackWrites.Load(), installer.rollbacks.Load())
	}
}

func TestCoordinatorRollbackFailureReleasesOwnedDrain(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	root := t.TempDir()
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := drain.SetPersistence(filepath.Join(root, "drain.json")); err != nil {
		t.Fatal(err)
	}
	var releaseWrites atomic.Int32
	drain.stateWriter = func(path string, data []byte) error {
		if bytes.Contains(data, []byte(`"state":"idle"`)) && releaseWrites.Add(1) == 1 {
			return errors.New("transient rollback cleanup failure")
		}
		return atomicWriteState(path, data)
	}
	installer := &phasePersistenceInstaller{applyErr: errors.New("apply failed"), rollbackErr: errors.New("rollback failed")}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionDocker}, "stable", drain, installer, false, "", nil)
	if err := coordinator.SetPersistence(filepath.Join(root, "coordinator.json")); err != nil {
		t.Fatal(err)
	}
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.staged = "staged"
	coordinator.state = StateAvailable
	if err := coordinator.Apply(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		snapshot := coordinator.Snapshot()
		if snapshot.State == StateFailed && snapshot.Drain.State == DrainStateIdle {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("rollback failure stranded drain: %#v", snapshot)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if installer.rollbacks.Load() != 1 {
		t.Fatalf("rollbacks=%d", installer.rollbacks.Load())
	}
	if releaseWrites.Load() < 2 {
		t.Fatalf("rollback cleanup writes=%d, want retry", releaseWrites.Load())
	}
}

func TestCoordinatorRecoveryReleasesDockerRollingBackOwnedDrain(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	root := t.TempDir()
	drainPath := filepath.Join(root, "drain.json")
	coordinatorPath := filepath.Join(root, "coordinator.json")
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := drain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	status, err := drain.BeginDrain(DrainRequest{Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !drain.TakeOwnership(status.Generation) {
		t.Fatal("failed to own test drain")
	}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionDocker}, "stable", drain, &DockerAgentInstaller{}, false, "", nil)
	coordinator.persistence = coordinatorPath
	coordinator.state = StateRollingBack
	coordinator.lastError = "agent update failed"
	coordinator.operationGeneration = status.Generation
	if err := coordinator.persistLocked(); err != nil {
		t.Fatal(err)
	}

	restartedDrain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := restartedDrain.SetPersistence(drainPath); err != nil {
		t.Fatal(err)
	}
	var releaseWrites atomic.Int32
	restartedDrain.stateWriter = func(path string, data []byte) error {
		if bytes.Contains(data, []byte(`"state":"idle"`)) && releaseWrites.Add(1) == 1 {
			return errors.New("transient restarted drain cleanup failure")
		}
		return atomicWriteState(path, data)
	}
	restarted := NewCoordinator(client, coordinator.current, "stable", restartedDrain, &DockerAgentInstaller{}, false, "", nil)
	if err := restarted.SetPersistence(coordinatorPath); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restarted.StartRecovery(ctx)
	waitForFailedIdle(t, restarted)
	if releaseWrites.Load() < 2 {
		t.Fatalf("restart cleanup writes = %d, want retry", releaseWrites.Load())
	}
	if snapshot := restarted.Snapshot(); snapshot.Error != "agent update failed" {
		t.Fatalf("recovery error = %q", snapshot.Error)
	}
}

func waitForFailedIdle(t *testing.T, coordinator *Coordinator) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		snapshot := coordinator.Snapshot()
		if snapshot.State == StateFailed && snapshot.Drain.State == DrainStateIdle {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("coordinator did not durably fail and release drain: %#v", snapshot)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCoordinatorRetriesTerminalDrainReleaseAfterSuccess(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	root := t.TempDir()
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := drain.SetPersistence(filepath.Join(root, "drain.json")); err != nil {
		t.Fatal(err)
	}
	var releaseWrites atomic.Int32
	drain.stateWriter = func(path string, data []byte) error {
		if bytes.Contains(data, []byte(`"state":"idle"`)) && releaseWrites.Add(1) == 1 {
			return errors.New("transient terminal release failure")
		}
		return atomicWriteState(path, data)
	}
	installer := &phasePersistenceInstaller{}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionDocker}, "stable", drain, installer, false, "", nil)
	if err := coordinator.SetPersistence(filepath.Join(root, "coordinator.json")); err != nil {
		t.Fatal(err)
	}
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.staged = "staged"
	coordinator.state = StateAvailable
	if err := coordinator.Apply(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for coordinator.Snapshot().State != StateSucceeded && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != StateSucceeded || snapshot.Drain.State != DrainStateIdle {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	if releaseWrites.Load() < 2 {
		t.Fatalf("terminal release writes=%d, want retry", releaseWrites.Load())
	}
}

func TestCoordinatorRetriesAbortDrainRelease(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	root := t.TempDir()
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := drain.SetPersistence(filepath.Join(root, "drain.json")); err != nil {
		t.Fatal(err)
	}
	var releaseWrites atomic.Int32
	drain.stateWriter = func(path string, data []byte) error {
		if bytes.Contains(data, []byte(`"state":"idle"`)) && releaseWrites.Add(1) == 1 {
			return errors.New("transient abort release failure")
		}
		return atomicWriteState(path, data)
	}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionDocker}, "stable", drain, nil, false, "", nil)
	if err := coordinator.SetPersistence(filepath.Join(root, "coordinator.json")); err != nil {
		t.Fatal(err)
	}
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.staged = "staged"
	coordinator.state = StateAvailable
	if err := coordinator.Apply(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for coordinator.Snapshot().State != StateFailed && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != StateFailed || snapshot.Drain.State != DrainStateIdle {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	if releaseWrites.Load() < 2 {
		t.Fatalf("abort release writes=%d, want retry", releaseWrites.Load())
	}
}

func TestCoordinatorRetriesTerminalCoordinatorPersistence(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	root := t.TempDir()
	drain := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := drain.SetPersistence(filepath.Join(root, "drain.json")); err != nil {
		t.Fatal(err)
	}
	installer := &phasePersistenceInstaller{}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionDocker}, "stable", drain, installer, false, "", nil)
	if err := coordinator.SetPersistence(filepath.Join(root, "coordinator.json")); err != nil {
		t.Fatal(err)
	}
	var succeededWrites atomic.Int32
	coordinator.stateWriter = func(path string, data []byte) error {
		if bytes.Contains(data, []byte(`"state":"succeeded"`)) && succeededWrites.Add(1) == 1 {
			return errors.New("transient terminal coordinator persistence failure")
		}
		return atomicWriteState(path, data)
	}
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	coordinator.staged = "staged"
	coordinator.state = StateAvailable
	if err := coordinator.Apply(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for coordinator.Snapshot().State != StateSucceeded && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != StateSucceeded || snapshot.Drain.State != DrainStateIdle {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	if succeededWrites.Load() < 2 {
		t.Fatalf("terminal coordinator writes=%d, want retry", succeededWrites.Load())
	}
}

func TestCoordinatorManualReadyPersistenceFailureStillExpiresAutonomously(t *testing.T) {
	var unix atomic.Int64
	unix.Store(1000)
	now := func() time.Time { return time.Unix(unix.Load(), 0).UTC() }
	root := t.TempDir()
	tracker := NewWorkTracker()
	drain := NewDrainManager(nil, nil, 0, now)
	drain.SetWorkTracker(tracker)
	if err := drain.SetPersistence(filepath.Join(root, "drain.json")); err != nil {
		t.Fatal(err)
	}
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: now})
	coordinator := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionDocker}, "stable", drain, nil, true, "", nil)
	if err := coordinator.SetPersistence(filepath.Join(root, "coordinator.json")); err != nil {
		t.Fatal(err)
	}
	readyAttempted := make(chan struct{})
	var readyWrites atomic.Int32
	coordinator.stateWriter = func(path string, data []byte) error {
		if bytes.Contains(data, []byte(`"state":"ready"`)) {
			if readyWrites.Add(1) == 1 {
				close(readyAttempted)
			}
			return errors.New("ready persistence unavailable")
		}
		return atomicWriteState(path, data)
	}
	coordinator.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now().Add(time.Hour)}}
	coordinator.state = StateAvailable
	if err := coordinator.Apply(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-readyAttempted:
	case <-time.After(3 * time.Second):
		t.Fatal("manual operation never attempted to persist ready state")
	}
	unix.Add(2)
	select {
	case <-drain.Reopened():
	case <-time.After(3 * time.Second):
		t.Fatal("manual drain did not expire while ready persistence was failing")
	}
	done, err := tracker.Start(WorkTask)
	if err != nil {
		t.Fatalf("admission remained closed after lease expiry: %v", err)
	}
	done()
	deadline := time.Now().Add(3 * time.Second)
	for coordinator.Snapshot().State != StateIdle && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != StateIdle || snapshot.Drain.State != DrainStateIdle {
		t.Fatalf("manual coordinator cleanup did not settle: %#v", snapshot)
	}
}
