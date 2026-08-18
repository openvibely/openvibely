package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

type BinaryHelperConfig struct {
	ParentPID                      int
	Current, Staged, Backup        string
	HealthURL, ExpectedVersion     string
	PreviousVersion, OutcomeID     string
	RunningVersion                 string
	RelaunchMetadataPath           string
	Recovery                       bool
	Arguments                      []string
	WorkingDirectory               string
	WaitTimeout, ValidationTimeout time.Duration
	StartCommand                   func(string, string) (func(context.Context) error, error)
	HealthClient                   *http.Client
}

const (
	binaryOutcomePrepared        = "prepared"
	binaryOutcomePending         = "pending"
	binaryOutcomeAuthorized      = "authorized"
	binaryOutcomeParentExited    = "parent_exited"
	binaryOutcomeBackupPublished = "backup_published"
	binaryOutcomeTargetPublished = "target_published"
	binaryOutcomeValidating      = "validating"
	binaryOutcomeRollingBack     = "rolling_back"
	binaryOutcomeRecovering      = "recovering"
	binaryOutcomeCancelled       = "cancelled"
	binaryOutcomeSucceeded       = "succeeded"
	binaryOutcomeRolledBack      = "rolled_back"
)

type binaryHelperOutcome struct {
	ID              string `json:"id"`
	State           string `json:"state"`
	PreviousVersion string `json:"previous_version"`
	DesiredVersion  string `json:"desired_version"`
}

func binaryHelperOutcomePath(current string) string { return current + ".openvibely-outcome.json" }
func binaryHelperPreparedPath(current string) string {
	return current + ".openvibely-outcome.prepared.json"
}
func binaryHelperAuthorizedPath(current string) string {
	return current + ".openvibely-outcome.authorized.json"
}
func binaryHelperCancelledPath(current string) string {
	return current + ".openvibely-outcome.cancelled.json"
}
func binaryHelperRecoveryReadyPath(current string) string {
	return current + ".openvibely-recovery-ready.json"
}
func binaryHelperRecoveryClaimPath(current string) string {
	return current + ".openvibely-recovery-claim.json"
}
func binaryHelperRelaunchMetadataPath(current string) string {
	return current + ".openvibely-relaunch.json"
}
func binaryHelperTransitionLeasePath(staged LocalStagedUpdate) string {
	digest := sha256.Sum256([]byte(staged.OutcomeID))
	return staged.InstallPath + ".openvibely-handoff-" + hex.EncodeToString(digest[:8]) + ".lock"
}
func binaryHelperLeasePath(staged LocalStagedUpdate) string {
	digest := sha256.Sum256([]byte(staged.OutcomeID))
	return staged.InstallPath + ".openvibely-helper-" + hex.EncodeToString(digest[:8]) + ".lock"
}

func writeBinaryHelperPhase(staged LocalStagedUpdate, state string) error {
	data, err := marshalBinaryHelperOutcome(staged, state)
	if err != nil {
		return err
	}
	return atomicWriteState(binaryHelperAuthorizedPath(staged.InstallPath), data)
}

func writeBinaryHelperPhaseWithRetry(ctx context.Context, staged LocalStagedUpdate, state string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := writeBinaryHelperPhase(staged, state); err == nil {
			return nil
		} else {
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return errors.Join(err, ctx.Err())
			case <-timer.C:
			}
		}
	}
}

func marshalBinaryHelperOutcome(staged LocalStagedUpdate, state string) ([]byte, error) {
	if staged.OutcomeID == "" || staged.PreviousVersion == "" || staged.Version == "" {
		return nil, errors.New("binary helper outcome identity is incomplete")
	}
	return json.Marshal(binaryHelperOutcome{ID: staged.OutcomeID, State: state, PreviousVersion: staged.PreviousVersion, DesiredVersion: staged.Version})
}

func writeBinaryHelperOutcome(staged LocalStagedUpdate, state string) error {
	data, err := marshalBinaryHelperOutcome(staged, state)
	if err != nil {
		return err
	}
	path := binaryHelperOutcomePath(staged.InstallPath)
	if state == binaryOutcomePrepared {
		path = binaryHelperPreparedPath(staged.InstallPath)
	}
	return atomicWriteState(path, data)
}

func claimBinaryHelperHandoff(ctx context.Context, staged LocalStagedUpdate) error {
	if err := os.Rename(binaryHelperPreparedPath(staged.InstallPath), binaryHelperOutcomePath(staged.InstallPath)); err != nil {
		return fmt.Errorf("claim binary helper handoff: %w", err)
	}
	return writeBinaryHelperOutcomeWithRetry(ctx, staged, binaryOutcomePending)
}

func authorizeBinaryHelperHandoff(staged LocalStagedUpdate) error {
	lease, err := acquireBinaryHelperTransitionLease(staged)
	if err != nil {
		return fmt.Errorf("authorize binary helper handoff: %w", err)
	}
	defer lease.Close()
	if err := renameBinaryHelperState(binaryHelperOutcomePath(staged.InstallPath), binaryHelperAuthorizedPath(staged.InstallPath)); err != nil {
		if os.IsNotExist(err) {
			outcome, readErr := readBinaryHelperOutcome(staged)
			if readErr == nil && outcome.State == binaryOutcomeAuthorized {
				return nil
			}
			if readErr == nil && outcome.State == binaryOutcomeCancelled {
				return errors.New("binary helper handoff was cancelled")
			}
		}
		return fmt.Errorf("authorize binary helper handoff: %w", err)
	}
	return nil
}

func cancelBinaryHelperHandoff(staged LocalStagedUpdate) (bool, error) {
	lease, err := acquireBinaryHelperTransitionLease(staged)
	if err != nil {
		return false, fmt.Errorf("cancel binary helper handoff: %w", err)
	}
	defer lease.Close()
	if err := renameBinaryHelperState(binaryHelperOutcomePath(staged.InstallPath), binaryHelperCancelledPath(staged.InstallPath)); err == nil {
		return true, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("cancel binary helper handoff: %w", err)
	}
	outcome, err := readBinaryHelperOutcome(staged)
	if err != nil {
		return false, err
	}
	switch outcome.State {
	case binaryOutcomeAuthorized:
		return false, nil
	case binaryOutcomeCancelled:
		return true, nil
	default:
		return false, fmt.Errorf("binary helper handoff has invalid terminal race state %q", outcome.State)
	}
}

func acquireBinaryHelperTransitionLease(staged LocalStagedUpdate) (*binaryHelperLease, error) {
	// Serializing the two terminal renames is required on Windows, where
	// concurrent MoveFileEx calls can otherwise both appear to claim the same
	// pending path.
	deadline := time.Now().Add(5 * time.Second)
	for {
		lease, acquired, err := tryAcquireBinaryHelperLease(binaryHelperTransitionLeasePath(staged))
		if err != nil {
			return nil, err
		}
		if acquired {
			return lease, nil
		}
		if time.Now().After(deadline) {
			return nil, errors.New("timed out acquiring binary helper handoff lease")
		}
		time.Sleep(time.Millisecond)
	}
}

func renameBinaryHelperState(source, destination string) error {
	deadline := time.Now().Add(time.Second)
	for {
		err := os.Rename(source, destination)
		if err == nil || os.IsNotExist(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(time.Millisecond)
	}
}

func cancelAuthorizedBinaryHelperHandoff(staged LocalStagedUpdate) error {
	for {
		if err := os.Rename(binaryHelperAuthorizedPath(staged.InstallPath), binaryHelperCancelledPath(staged.InstallPath)); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		outcome, err := readBinaryHelperOutcome(staged)
		if err == nil && outcome.State == binaryOutcomeCancelled {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func recoverAuthorizedParentExitFailure(cfg BinaryHelperConfig, staged LocalStagedUpdate, cause error) error {
	result := cause
	if err := ensureBootableBinary(cfg.Current, cfg.Backup); err != nil {
		return errors.Join(result, fmt.Errorf("binary recovery has no bootable executable: %w", err))
	}
	if err := cancelAuthorizedBinaryHelperHandoff(staged); err != nil {
		return errors.Join(result, fmt.Errorf("cancel binary helper after parent-exit failure: %w", err))
	}
	start := cfg.StartCommand
	if start == nil {
		start = packagedRestartCommand(cfg)
	}
	if _, err := start("exec", cfg.Current); err != nil {
		result = errors.Join(result, fmt.Errorf("binary recovery restart failed: %w", err))
	}
	return result
}

func waitForBinaryHelperAuthorization(ctx context.Context, staged LocalStagedUpdate, timeout time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		outcome, err := readBinaryHelperOutcome(staged)
		if err == nil {
			switch outcome.State {
			case binaryOutcomeAuthorized:
				return nil
			case binaryOutcomePending:
			case binaryOutcomeCancelled:
				return errors.New("binary helper handoff was cancelled")
			default:
				return fmt.Errorf("binary helper authorization has invalid state %q", outcome.State)
			}
		} else if os.IsNotExist(err) {
			return errors.New("binary helper handoff was revoked")
		} else {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			cancelled, cancelErr := cancelBinaryHelperHandoff(staged)
			if cancelErr != nil {
				return errors.Join(fmt.Errorf("wait for binary helper authorization: %w", ctx.Err()), cancelErr)
			}
			if !cancelled {
				return nil
			}
			return fmt.Errorf("wait for binary helper authorization: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func writeBinaryHelperOutcomeWithRetry(ctx context.Context, staged LocalStagedUpdate, state string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		err := writeBinaryHelperOutcome(staged, state)
		if err == nil {
			return nil
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(err, ctx.Err())
		case <-timer.C:
		}
	}
}

func writeBinaryHelperRecoveryClaim(staged LocalStagedUpdate) error {
	data, err := marshalBinaryHelperOutcome(staged, binaryOutcomeRecovering)
	if err != nil {
		return err
	}
	return atomicWriteState(binaryHelperRecoveryClaimPath(staged.InstallPath), data)
}

func writeBinaryHelperRecoveryClaimWithRetry(ctx context.Context, staged LocalStagedUpdate) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := writeBinaryHelperRecoveryClaim(staged); err == nil {
			return nil
		} else {
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return errors.Join(err, ctx.Err())
			case <-timer.C:
			}
		}
	}
}

func readBinaryHelperRecoveryClaim(staged LocalStagedUpdate) (binaryHelperOutcome, error) {
	return readBinaryHelperOutcomeAt(staged, binaryHelperRecoveryClaimPath(staged.InstallPath))
}

func removeBinaryHelperOutcome(staged LocalStagedUpdate) error {
	var result error
	for _, path := range []string{
		binaryHelperOutcomePath(staged.InstallPath),
		binaryHelperPreparedPath(staged.InstallPath),
		binaryHelperAuthorizedPath(staged.InstallPath),
		binaryHelperCancelledPath(staged.InstallPath),
		binaryHelperRecoveryClaimPath(staged.InstallPath),
		binaryHelperRecoveryReadyPath(staged.InstallPath),
		binaryHelperTransitionLeasePath(staged),
	} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			result = errors.Join(result, err)
		}
	}
	return result
}

func readBinaryHelperOutcome(staged LocalStagedUpdate) (binaryHelperOutcome, error) {
	outcome, err := readBinaryHelperOutcomeAt(staged, binaryHelperOutcomePath(staged.InstallPath))
	if err == nil || !os.IsNotExist(err) {
		return outcome, err
	}
	outcome, err = readBinaryHelperOutcomeAt(staged, binaryHelperAuthorizedPath(staged.InstallPath))
	if err == nil {
		if outcome.State == binaryOutcomePending {
			outcome.State = binaryOutcomeAuthorized
		}
		return outcome, nil
	}
	if !os.IsNotExist(err) {
		return outcome, err
	}
	outcome, err = readBinaryHelperOutcomeAt(staged, binaryHelperCancelledPath(staged.InstallPath))
	if err == nil {
		outcome.State = binaryOutcomeCancelled
		return outcome, nil
	}
	if !os.IsNotExist(err) {
		return outcome, err
	}
	// Authorization may have atomically claimed the active pending identity
	// after the first authorized-path read. Recheck it before reporting that no
	// durable winner exists.
	outcome, authorizedErr := readBinaryHelperOutcomeAt(staged, binaryHelperAuthorizedPath(staged.InstallPath))
	if authorizedErr == nil {
		if outcome.State == binaryOutcomePending {
			outcome.State = binaryOutcomeAuthorized
		}
		return outcome, nil
	}
	return outcome, authorizedErr
}

func readBinaryHelperPrepared(staged LocalStagedUpdate) (binaryHelperOutcome, error) {
	return readBinaryHelperOutcomeAt(staged, binaryHelperPreparedPath(staged.InstallPath))
}

func readBinaryHelperOutcomeAt(staged LocalStagedUpdate, path string) (binaryHelperOutcome, error) {
	var outcome binaryHelperOutcome
	if staged.OutcomeID == "" || staged.PreviousVersion == "" || staged.Version == "" {
		return outcome, errors.New("binary helper outcome identity is incomplete")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return outcome, err
	}
	if err := json.Unmarshal(data, &outcome); err != nil {
		return outcome, err
	}
	if outcome.ID != staged.OutcomeID || outcome.PreviousVersion != staged.PreviousVersion || outcome.DesiredVersion != staged.Version {
		return binaryHelperOutcome{}, errors.New("binary helper outcome identity does not match the staged update")
	}
	switch outcome.State {
	case binaryOutcomePrepared, binaryOutcomePending, binaryOutcomeAuthorized,
		binaryOutcomeParentExited, binaryOutcomeBackupPublished, binaryOutcomeTargetPublished,
		binaryOutcomeValidating, binaryOutcomeRollingBack, binaryOutcomeRecovering,
		binaryOutcomeCancelled, binaryOutcomeSucceeded, binaryOutcomeRolledBack:
		return outcome, nil
	default:
		return binaryHelperOutcome{}, errors.New("binary helper outcome state is invalid")
	}
}

func RunBinaryHelper(ctx context.Context, cfg BinaryHelperConfig) (runErr error) {
	staged := LocalStagedUpdate{ArtifactPath: cfg.Staged, InstallPath: cfg.Current, BackupPath: cfg.Backup, Version: cfg.ExpectedVersion, PreviousVersion: cfg.PreviousVersion, OutcomeID: cfg.OutcomeID}
	if err := validateBinaryPaths(staged); err != nil {
		return err
	}
	if cfg.ParentPID <= 1 || cfg.ParentPID == os.Getpid() {
		return errors.New("invalid parent process ID")
	}
	if cfg.HealthURL == "" || cfg.ExpectedVersion == "" {
		return errors.New("health URL and expected version are required")
	}
	outcomeEnabled := cfg.OutcomeID != "" || cfg.PreviousVersion != ""
	if outcomeEnabled && (cfg.OutcomeID == "" || cfg.PreviousVersion == "") {
		return errors.New("binary helper outcome identity is incomplete")
	}
	if cfg.WaitTimeout <= 0 {
		cfg.WaitTimeout = 30 * time.Second
	}
	if cfg.ValidationTimeout <= 0 {
		cfg.ValidationTimeout = 60 * time.Second
	}
	phase := binaryOutcomeAuthorized
	var lease *binaryHelperLease
	if outcomeEnabled {
		var acquired bool
		var err error
		lease, acquired, err = tryAcquireBinaryHelperLease(binaryHelperLeasePath(staged))
		if err != nil {
			return fmt.Errorf("acquire binary helper lease: %w", err)
		}
		if !acquired {
			return errors.New("binary helper lease is already owned")
		}
		defer lease.Close()
		_, recoveryClaimErr := readBinaryHelperRecoveryClaim(staged)
		switch {
		case recoveryClaimErr == nil && !cfg.Recovery:
			// Startup recovery durably claimed this exact operation while proving
			// the prior helper dead. A manager retry of the original helper must
			// exit without resuming replacement side effects.
			return nil
		case recoveryClaimErr == nil:
		case os.IsNotExist(recoveryClaimErr) && cfg.Recovery:
			return errors.New("binary recovery helper ownership claim is missing")
		case os.IsNotExist(recoveryClaimErr):
		default:
			return fmt.Errorf("read binary recovery ownership claim: %w", recoveryClaimErr)
		}
		if cfg.Recovery {
			return runBinaryRecoveryHelper(ctx, cfg, staged)
		}

		if _, err := readBinaryHelperPrepared(staged); err == nil {
			if err := claimBinaryHelperHandoff(ctx, staged); err != nil {
				return err
			}
			if err := waitForBinaryHelperAuthorization(ctx, staged, cfg.WaitTimeout); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		outcome, err := readBinaryHelperOutcome(staged)
		if err != nil {
			return err
		}
		if outcome.State == binaryOutcomePending {
			if err := waitForBinaryHelperAuthorization(ctx, staged, cfg.WaitTimeout); err != nil {
				return err
			}
			outcome, err = readBinaryHelperOutcome(staged)
			if err != nil {
				return err
			}
		}
		switch outcome.State {
		case binaryOutcomeCancelled, binaryOutcomeSucceeded, binaryOutcomeRolledBack:
			return nil
		case binaryOutcomeAuthorized, binaryOutcomeParentExited, binaryOutcomeBackupPublished,
			binaryOutcomeTargetPublished, binaryOutcomeValidating, binaryOutcomeRollingBack:
			phase = outcome.State
		default:
			return fmt.Errorf("binary helper cannot resume phase %q", outcome.State)
		}
	}

	if !outcomeEnabled || phase == binaryOutcomeAuthorized {
		if err := waitForProcessExit(ctx, cfg.ParentPID, cfg.WaitTimeout); err != nil {
			if outcomeEnabled {
				return recoverAuthorizedParentExitFailure(cfg, staged, err)
			}
			return err
		}
		phase = binaryOutcomeParentExited
		if outcomeEnabled {
			if err := writeBinaryHelperPhaseWithRetry(ctx, staged, phase); err != nil {
				return fmt.Errorf("persist binary helper parent-exit phase: %w", err)
			}
		}
	}

	start := cfg.StartCommand
	if start == nil {
		start = packagedRestartCommand(cfg)
	}
	recoveryComplete := false
	replacementPublished := phase == binaryOutcomeTargetPublished || phase == binaryOutcomeValidating
	defer func() {
		if runErr == nil || recoveryComplete {
			return
		}
		if err := ensureBootableBinary(cfg.Current, cfg.Backup); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("binary recovery has no bootable executable: %w", err))
			return
		}
		if _, err := start("exec", cfg.Current); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("binary recovery restart failed: %w", err))
			return
		}
		if outcomeEnabled && !replacementPublished {
			if err := writeBinaryHelperOutcomeWithRetry(ctx, staged, binaryOutcomeRolledBack); err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("persist binary rollback outcome: %w", err))
				return
			}
		}
		recoveryComplete = true
	}()

	if phase == binaryOutcomeRollingBack {
		if _, err := os.Stat(cfg.Backup); err == nil {
			if err := atomicReplace(cfg.Backup, cfg.Current); err != nil {
				return fmt.Errorf("resume binary rollback: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := syncDirectory(filepath.Dir(cfg.Current)); err != nil {
			return fmt.Errorf("sync resumed binary rollback: %w", err)
		}
		if _, err := start("exec", cfg.Current); err != nil {
			return fmt.Errorf("resume binary rollback restart: %w", err)
		}
		if outcomeEnabled {
			if err := writeBinaryHelperOutcomeWithRetry(ctx, staged, binaryOutcomeRolledBack); err != nil {
				return err
			}
		}
		recoveryComplete = true
		return errors.New("resumed binary rollback completed")
	}

	if phase == binaryOutcomeParentExited {
		if err := recoverInterruptedBinarySwap(cfg.Current, cfg.Backup); err != nil {
			return err
		}
		info, err := os.Stat(cfg.Current)
		if err != nil {
			return err
		}
		newInfo, err := os.Stat(cfg.Staged)
		if err != nil {
			return err
		}
		if newInfo.IsDir() {
			return errors.New("staged binary is a directory")
		}
		if err := os.Chmod(cfg.Staged, info.Mode().Perm()); err != nil {
			return err
		}
		if err := publishBinaryBackup(cfg.Current, cfg.Backup, info.Mode().Perm()); err != nil {
			return err
		}
		phase = binaryOutcomeBackupPublished
		if outcomeEnabled {
			if err := writeBinaryHelperPhaseWithRetry(ctx, staged, phase); err != nil {
				return fmt.Errorf("persist binary backup phase: %w", err)
			}
		}
	}
	if phase == binaryOutcomeBackupPublished {
		if _, err := os.Stat(cfg.Staged); err == nil {
			if err := publishStagedBinary(cfg.Current, cfg.Staged); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		phase = binaryOutcomeTargetPublished
		replacementPublished = true
		if outcomeEnabled {
			if err := writeBinaryHelperPhaseWithRetry(ctx, staged, phase); err != nil {
				return fmt.Errorf("persist binary target phase: %w", err)
			}
		}
	}
	if phase == binaryOutcomeTargetPublished {
		phase = binaryOutcomeValidating
		if outcomeEnabled {
			if err := writeBinaryHelperPhaseWithRetry(ctx, staged, phase); err != nil {
				return fmt.Errorf("persist binary validation phase: %w", err)
			}
		}
	}

	stopSuccessor, restartErr := start("exec", cfg.Current)
	successorStarted := restartErr == nil
	if successorStarted {
		validationCtx, cancel := context.WithTimeout(ctx, cfg.ValidationTimeout)
		restartErr = waitForExpectedHealth(validationCtx, cfg.HealthURL, cfg.ExpectedVersion, cfg.HealthClient)
		cancel()
	}
	if restartErr == nil {
		if outcomeEnabled {
			if err := writeBinaryHelperOutcomeWithRetry(ctx, staged, binaryOutcomeSucceeded); err != nil {
				return fmt.Errorf("persist binary success outcome: %w", err)
			}
		}
		return nil
	}
	err := restartErr
	if successorStarted {
		if stopSuccessor == nil {
			return fmt.Errorf("validation failed: %v; failed successor shutdown is unavailable", err)
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), cfg.WaitTimeout)
		stopErr := stopSuccessor(stopCtx)
		cancel()
		if stopErr != nil {
			return fmt.Errorf("validation failed: %v; stopping failed successor: %w", err, stopErr)
		}
	}
	if outcomeEnabled {
		if phaseErr := writeBinaryHelperPhaseWithRetry(ctx, staged, binaryOutcomeRollingBack); phaseErr != nil {
			return fmt.Errorf("validation failed: %v; persist binary rollback phase: %w", err, phaseErr)
		}
	}
	replacementPublished = false
	if rollbackErr := atomicReplace(cfg.Backup, cfg.Current); rollbackErr != nil {
		return fmt.Errorf("validation failed: %v; rollback failed: %w", err, rollbackErr)
	}
	if syncErr := syncDirectory(filepath.Dir(cfg.Current)); syncErr != nil {
		return fmt.Errorf("validation failed: %v; syncing rollback: %w", err, syncErr)
	}
	if _, restartErr := start("exec", cfg.Current); restartErr != nil {
		return fmt.Errorf("validation failed: %v; rollback restart failed: %w", err, restartErr)
	}
	if outcomeEnabled {
		if outcomeErr := writeBinaryHelperOutcomeWithRetry(ctx, staged, binaryOutcomeRolledBack); outcomeErr != nil {
			return fmt.Errorf("validation failed: %v; persist binary rollback outcome: %w", err, outcomeErr)
		}
	}
	recoveryComplete = true
	return fmt.Errorf("new binary validation failed and prior binary was restored: %w", err)
}

func runBinaryRecoveryHelper(ctx context.Context, cfg BinaryHelperConfig, staged LocalStagedUpdate) error {
	outcome, err := readBinaryHelperOutcome(staged)
	if err != nil {
		return fmt.Errorf("read binary recovery phase: %w", err)
	}
	switch outcome.State {
	case binaryOutcomeCancelled, binaryOutcomeSucceeded, binaryOutcomeRolledBack:
		return nil
	case binaryOutcomeAuthorized, binaryOutcomeParentExited, binaryOutcomeBackupPublished,
		binaryOutcomeTargetPublished, binaryOutcomeValidating, binaryOutcomeRollingBack:
	default:
		return fmt.Errorf("binary recovery has invalid phase %q", outcome.State)
	}
	ready, err := marshalBinaryHelperOutcome(staged, binaryOutcomeRecovering)
	if err != nil {
		return err
	}
	if err := atomicWriteState(binaryHelperRecoveryReadyPath(staged.InstallPath), ready); err != nil {
		return fmt.Errorf("publish binary recovery readiness: %w", err)
	}
	if err := waitForProcessExit(ctx, cfg.ParentPID, cfg.WaitTimeout); err != nil {
		return err
	}
	start := cfg.StartCommand
	if start == nil {
		start = packagedRestartCommand(cfg)
	}
	if outcome.State == binaryOutcomeRollingBack {
		if _, err := os.Stat(cfg.Backup); err == nil {
			if err := atomicReplace(cfg.Backup, cfg.Current); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := syncDirectory(filepath.Dir(cfg.Current)); err != nil {
			return err
		}
		if _, err := start("exec", cfg.Current); err != nil {
			return err
		}
		return writeBinaryHelperOutcomeWithRetry(ctx, staged, binaryOutcomeRolledBack)
	}
	if cfg.RunningVersion == staged.PreviousVersion {
		if _, err := os.Stat(staged.ArtifactPath); err == nil {
			if err := ensureBootableBinary(cfg.Current, cfg.Backup); err != nil {
				return err
			}
			if _, err := start("exec", cfg.Current); err != nil {
				return err
			}
			return writeBinaryHelperOutcomeWithRetry(ctx, staged, binaryOutcomeCancelled)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return recoverPublishedBinaryTarget(ctx, cfg, staged, start)
}

func recoverPublishedBinaryTarget(ctx context.Context, cfg BinaryHelperConfig, staged LocalStagedUpdate, start func(string, string) (func(context.Context) error, error)) error {
	stopSuccessor, restartErr := start("exec", cfg.Current)
	if restartErr == nil {
		validationCtx, cancel := context.WithTimeout(ctx, cfg.ValidationTimeout)
		restartErr = waitForExpectedHealth(validationCtx, cfg.HealthURL, cfg.ExpectedVersion, cfg.HealthClient)
		cancel()
	}
	if restartErr == nil {
		return writeBinaryHelperOutcomeWithRetry(ctx, staged, binaryOutcomeSucceeded)
	}
	if stopSuccessor != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), cfg.WaitTimeout)
		stopErr := stopSuccessor(stopCtx)
		cancel()
		if stopErr != nil {
			return errors.Join(restartErr, fmt.Errorf("stopping failed recovery successor: %w", stopErr))
		}
	}
	if err := writeBinaryHelperPhaseWithRetry(ctx, staged, binaryOutcomeRollingBack); err != nil {
		return errors.Join(restartErr, err)
	}
	if err := atomicReplace(cfg.Backup, cfg.Current); err != nil {
		return errors.Join(restartErr, err)
	}
	if err := syncDirectory(filepath.Dir(cfg.Current)); err != nil {
		return errors.Join(restartErr, err)
	}
	if _, err := start("exec", cfg.Current); err != nil {
		return errors.Join(restartErr, err)
	}
	if err := writeBinaryHelperOutcomeWithRetry(ctx, staged, binaryOutcomeRolledBack); err != nil {
		return errors.Join(restartErr, err)
	}
	return fmt.Errorf("recovered binary target failed validation and predecessor was restored: %w", restartErr)
}

func ensureBootableBinary(current, backup string) error {
	info, err := os.Stat(current)
	if err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("current binary is not a regular file")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return recoverInterruptedBinarySwap(current, backup)
}

func recoverInterruptedBinarySwap(current, backup string) error {
	if _, err := os.Stat(current); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	info, err := os.Stat(backup)
	if err != nil {
		return fmt.Errorf("current binary is missing and no rollback backup is available: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("binary rollback backup is not a regular file")
	}
	if err := os.Rename(backup, current); err != nil {
		return fmt.Errorf("restore interrupted binary swap: %w", err)
	}
	return syncDirectory(filepath.Dir(current))
}

func installStagedBinary(current, staged, backup string, mode os.FileMode) error {
	if err := publishBinaryBackup(current, backup, mode); err != nil {
		return err
	}
	return publishStagedBinary(current, staged)
}

func publishStagedBinary(current, staged string) error {
	// FlushFileBuffers requires a write-capable handle on Windows.
	stagedFile, err := os.OpenFile(staged, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	syncErr := stagedFile.Sync()
	closeErr := stagedFile.Close()
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := atomicReplace(staged, current); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(current))
}

func publishBinaryBackup(current, backup string, mode os.FileMode) (err error) {
	partial := backup + ".partial"
	if err := os.Remove(partial); err != nil && !os.IsNotExist(err) {
		return err
	}
	input, err := os.Open(current)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(partial)
		}
	}()
	if _, err = io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	if err = os.Chmod(partial, mode); err != nil {
		_ = output.Close()
		return err
	}
	if err = output.Sync(); err != nil {
		_ = output.Close()
		return err
	}
	if err = output.Close(); err != nil {
		return err
	}
	if err = atomicReplace(partial, backup); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(backup))
}

var waitForProcessExit = waitForProcessExitPlatform

func packagedRestartCommand(cfg BinaryHelperConfig) func(string, string) (func(context.Context) error, error) {
	return func(_, _ string) (func(context.Context) error, error) {
		arguments := cfg.Arguments
		if len(arguments) == 0 {
			arguments = []string{cfg.Current}
		}
		cmd := exec.Command(cfg.Current, arguments[1:]...)
		cmd.Args[0] = arguments[0]
		cmd.Dir = cfg.WorkingDirectory
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return func(context.Context) error { return stopStartedProcess(cmd) }, nil
	}
}

func stopStartedProcess(cmd *exec.Cmd) error {
	killErr := cmd.Process.Kill()
	if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
		if os.IsPermission(killErr) {
			if waitErr := waitStartedCommand(cmd, 2*time.Second); waitErr == nil || isExpectedProcessExit(waitErr) {
				return nil
			}
		}
		return killErr
	}
	waitErr := waitStartedCommand(cmd, 0)
	if waitErr == nil || isExpectedProcessExit(waitErr) {
		return nil
	}
	return waitErr
}

func waitStartedCommand(cmd *exec.Cmd, timeout time.Duration) error {
	if timeout <= 0 {
		return cmd.Wait()
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return context.DeadlineExceeded
	}
}

func isExpectedProcessExit(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

func waitForExpectedHealth(ctx context.Context, endpoint, expected string, client *http.Client) error {
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			var body struct {
				Ready   bool   `json:"ready"`
				Version string `json:"version"`
			}
			decodeErr := json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && decodeErr == nil && body.Ready && body.Version == expected {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type binaryRelaunchMetadata struct {
	Arguments          []string `json:"arguments"`
	WorkingDirectory   string   `json:"working_directory"`
	ExecutableRelative string   `json:"executable_relative,omitempty"`
}

func LoadBinaryHelperRelaunch(reader io.Reader, cfg *BinaryHelperConfig) error {
	if reader == nil || cfg == nil {
		return errors.New("binary helper relaunch metadata is unavailable")
	}
	var metadata binaryRelaunchMetadata
	decoder := json.NewDecoder(io.LimitReader(reader, 1024*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return fmt.Errorf("decode binary helper relaunch metadata: %w", err)
	}
	if len(metadata.Arguments) == 0 || metadata.WorkingDirectory == "" || !filepath.IsAbs(metadata.WorkingDirectory) {
		return errors.New("binary helper relaunch metadata is incomplete")
	}
	cfg.Arguments = append([]string(nil), metadata.Arguments...)
	cfg.WorkingDirectory = metadata.WorkingDirectory
	return nil
}

func LoadBinaryHelperRelaunchFile(path string, cfg *BinaryHelperConfig) error {
	if path == "" || cfg == nil {
		return errors.New("binary helper relaunch metadata path is unavailable")
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open binary helper relaunch metadata: %w", err)
	}
	defer os.Remove(path)
	defer file.Close()
	return LoadBinaryHelperRelaunch(file, cfg)
}

func ParseBinaryHelperArgs(args []string) (BinaryHelperConfig, error) {
	allowed := map[string]bool{
		"--parent-pid": true, "--current": true, "--staged": true, "--backup": true,
		"--health-url": true, "--expected-version": true, "--previous-version": true,
		"--outcome-id": true, "--running-version": true, "--recovery": true,
		"--relaunch-metadata": true,
	}
	values := map[string]string{}
	for len(args) > 0 {
		if len(args) < 2 || len(args[0]) < 3 || args[0][:2] != "--" {
			return BinaryHelperConfig{}, errors.New("invalid update-helper arguments")
		}
		if !allowed[args[0]] {
			return BinaryHelperConfig{}, fmt.Errorf("unsupported update-helper argument %s", args[0])
		}
		if _, exists := values[args[0]]; exists {
			return BinaryHelperConfig{}, errors.New("duplicate update-helper argument")
		}
		values[args[0]] = args[1]
		args = args[2:]
	}
	pid, err := strconv.Atoi(values["--parent-pid"])
	if err != nil {
		return BinaryHelperConfig{}, errors.New("invalid parent PID")
	}
	cfg := BinaryHelperConfig{ParentPID: pid, Current: values["--current"], Staged: values["--staged"], Backup: values["--backup"], HealthURL: values["--health-url"], ExpectedVersion: values["--expected-version"], PreviousVersion: values["--previous-version"], OutcomeID: values["--outcome-id"], RunningVersion: values["--running-version"], RelaunchMetadataPath: values["--relaunch-metadata"], Recovery: values["--recovery"] == "true"}
	if values["--recovery"] != "" && values["--recovery"] != "true" {
		return BinaryHelperConfig{}, errors.New("invalid recovery mode")
	}
	if cfg.Recovery && cfg.RunningVersion == "" {
		return BinaryHelperConfig{}, errors.New("binary recovery running version is required")
	}
	if cfg.PreviousVersion == "" || cfg.OutcomeID == "" {
		return BinaryHelperConfig{}, errors.New("binary helper outcome identity is required")
	}
	for _, path := range []string{cfg.Current, cfg.Staged, cfg.Backup} {
		if !filepath.IsAbs(path) {
			return BinaryHelperConfig{}, errors.New("update-helper paths must be absolute")
		}
	}
	if cfg.RelaunchMetadataPath != "" && !filepath.IsAbs(cfg.RelaunchMetadataPath) {
		return BinaryHelperConfig{}, errors.New("update-helper relaunch metadata path must be absolute")
	}
	return cfg, nil
}
