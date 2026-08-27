package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

type ExecutableUpdateHelperConfig struct {
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
	packagedUpdateOutcomePrepared        = "prepared"
	packagedUpdateOutcomePending         = "pending"
	packagedUpdateOutcomeAuthorized      = "authorized"
	packagedUpdateOutcomeParentExited    = "parent_exited"
	packagedUpdateOutcomeBackupPublished = "backup_published"
	packagedUpdateOutcomeTargetPublished = "target_published"
	packagedUpdateOutcomeValidating      = "validating"
	packagedUpdateOutcomeRollingBack     = "rolling_back"
	packagedUpdateOutcomeRecovering      = "recovering"
	packagedUpdateOutcomeCancelled       = "cancelled"
	packagedUpdateOutcomeSucceeded       = "succeeded"
	packagedUpdateOutcomeRolledBack      = "rolled_back"
)

type packagedUpdateHelperOutcome struct {
	ID              string `json:"id"`
	State           string `json:"state"`
	PreviousVersion string `json:"previous_version"`
	DesiredVersion  string `json:"desired_version"`
}

func packagedUpdateHelperOutcomePath(current string) string {
	return current + ".openvibely-outcome.json"
}
func packagedUpdateHelperPreparedPath(current string) string {
	return current + ".openvibely-outcome.prepared.json"
}
func packagedUpdateHelperAuthorizedPath(current string) string {
	return current + ".openvibely-outcome.authorized.json"
}
func packagedUpdateHelperCancelledPath(current string) string {
	return current + ".openvibely-outcome.cancelled.json"
}
func packagedUpdateHelperRecoveryReadyPath(current string) string {
	return current + ".openvibely-recovery-ready.json"
}
func packagedUpdateHelperRecoveryClaimPath(current string) string {
	return current + ".openvibely-recovery-claim.json"
}
func packagedUpdateHelperRelaunchMetadataPath(current string) string {
	return current + ".openvibely-relaunch.json"
}
func packagedUpdateReinstallCancellationPath(current string) string {
	return current + ".openvibely-reinstall-cancel"
}
func packagedUpdateHelperTransitionLeasePath(staged LocalStagedUpdate) string {
	digest := sha256.Sum256([]byte(staged.OutcomeID))
	return staged.InstallPath + ".openvibely-handoff-" + hex.EncodeToString(digest[:8]) + ".lock"
}
func packagedUpdateHelperLeasePath(staged LocalStagedUpdate) string {
	digest := sha256.Sum256([]byte(staged.OutcomeID))
	// Keep the persisted lease filename stable across releases so a successor
	// cannot mistake an older, still-running helper for a dead handoff.
	return staged.InstallPath + ".openvibely-helper-" + hex.EncodeToString(digest[:8]) + ".lock"
}

func packagedUpdateReinstallRequested(staged LocalStagedUpdate) (bool, error) {
	if staged.OutcomeID == "" || staged.InstallPath == "" {
		return false, nil
	}
	data, err := os.ReadFile(packagedUpdateReinstallCancellationPath(staged.InstallPath))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return string(bytes.TrimSpace(data)) == staged.OutcomeID, nil
}

func writePackagedUpdateHelperPhase(staged LocalStagedUpdate, state string) error {
	data, err := marshalPackagedUpdateHelperOutcome(staged, state)
	if err != nil {
		return err
	}
	return atomicWriteState(packagedUpdateHelperAuthorizedPath(staged.InstallPath), data)
}

func writePackagedUpdateHelperPhaseWithRetry(ctx context.Context, staged LocalStagedUpdate, state string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := writePackagedUpdateHelperPhase(staged, state); err == nil {
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

func marshalPackagedUpdateHelperOutcome(staged LocalStagedUpdate, state string) ([]byte, error) {
	if staged.OutcomeID == "" || staged.PreviousVersion == "" || staged.Version == "" {
		return nil, errors.New("packaged update helper outcome identity is incomplete")
	}
	return json.Marshal(packagedUpdateHelperOutcome{ID: staged.OutcomeID, State: state, PreviousVersion: staged.PreviousVersion, DesiredVersion: staged.Version})
}

func writePackagedUpdateHelperOutcome(staged LocalStagedUpdate, state string) error {
	data, err := marshalPackagedUpdateHelperOutcome(staged, state)
	if err != nil {
		return err
	}
	path := packagedUpdateHelperOutcomePath(staged.InstallPath)
	if state == packagedUpdateOutcomePrepared {
		path = packagedUpdateHelperPreparedPath(staged.InstallPath)
	}
	return atomicWriteState(path, data)
}

func claimPackagedUpdateHelperHandoff(ctx context.Context, staged LocalStagedUpdate) error {
	lease, err := acquirePackagedUpdateHelperTransitionLease(staged)
	if err != nil {
		return fmt.Errorf("claim packaged update helper handoff: %w", err)
	}
	defer lease.Close()
	prepared, err := readPackagedUpdateHelperPrepared(staged)
	if err != nil {
		return fmt.Errorf("claim packaged update helper handoff: %w", err)
	}
	if prepared.State != packagedUpdateOutcomePrepared && prepared.State != packagedUpdateOutcomePending {
		return fmt.Errorf("claim packaged update helper handoff: invalid prepared state %q", prepared.State)
	}
	data, err := marshalPackagedUpdateHelperOutcome(staged, packagedUpdateOutcomePending)
	if err != nil {
		return fmt.Errorf("claim packaged update helper handoff: %w", err)
	}
	if err := writePackagedUpdateHelperStateWithRetry(ctx, packagedUpdateHelperPreparedPath(staged.InstallPath), data); err != nil {
		return fmt.Errorf("claim packaged update helper handoff: %w", err)
	}
	if err := renamePackagedUpdateHelperState(packagedUpdateHelperPreparedPath(staged.InstallPath), packagedUpdateHelperOutcomePath(staged.InstallPath)); err != nil {
		return fmt.Errorf("claim packaged update helper handoff: %w", err)
	}
	return nil
}

func cancelPreparedPackagedUpdateHelperHandoff(staged LocalStagedUpdate) (bool, error) {
	lease, err := acquirePackagedUpdateHelperTransitionLease(staged)
	if err != nil {
		return false, fmt.Errorf("cancel prepared packaged update helper handoff: %w", err)
	}
	defer lease.Close()
	prepared, err := readPackagedUpdateHelperPrepared(staged)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if prepared.State != packagedUpdateOutcomePrepared && prepared.State != packagedUpdateOutcomePending {
		return false, fmt.Errorf("prepared packaged update helper handoff has invalid state %q", prepared.State)
	}
	if err := os.Remove(packagedUpdateHelperPreparedPath(staged.InstallPath)); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func authorizePackagedUpdateHelperHandoff(staged LocalStagedUpdate) error {
	lease, err := acquirePackagedUpdateHelperTransitionLease(staged)
	if err != nil {
		return fmt.Errorf("authorize packaged update helper handoff: %w", err)
	}
	defer lease.Close()
	if err := renamePackagedUpdateHelperState(packagedUpdateHelperOutcomePath(staged.InstallPath), packagedUpdateHelperAuthorizedPath(staged.InstallPath)); err != nil {
		if os.IsNotExist(err) {
			outcome, readErr := readPackagedUpdateHelperOutcome(staged)
			if readErr == nil && outcome.State == packagedUpdateOutcomeAuthorized {
				return nil
			}
			if readErr == nil && outcome.State == packagedUpdateOutcomeCancelled {
				return errors.New("packaged update helper handoff was cancelled")
			}
		}
		return fmt.Errorf("authorize packaged update helper handoff: %w", err)
	}
	return nil
}

func cancelPackagedUpdateHelperHandoff(staged LocalStagedUpdate) (bool, error) {
	lease, err := acquirePackagedUpdateHelperTransitionLease(staged)
	if err != nil {
		return false, fmt.Errorf("cancel packaged update helper handoff: %w", err)
	}
	defer lease.Close()
	if err := renamePackagedUpdateHelperState(packagedUpdateHelperOutcomePath(staged.InstallPath), packagedUpdateHelperCancelledPath(staged.InstallPath)); err == nil {
		return true, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("cancel packaged update helper handoff: %w", err)
	}
	outcome, err := readPackagedUpdateHelperOutcome(staged)
	if err != nil {
		return false, err
	}
	switch outcome.State {
	case packagedUpdateOutcomeAuthorized:
		return false, nil
	case packagedUpdateOutcomeCancelled:
		return true, nil
	default:
		return false, fmt.Errorf("packaged update helper handoff has invalid terminal race state %q", outcome.State)
	}
}

func acquirePackagedUpdateHelperTransitionLease(staged LocalStagedUpdate) (*packagedUpdateHelperLease, error) {
	// Serializing the two terminal renames is required on Windows, where
	// concurrent MoveFileEx calls can otherwise both appear to claim the same
	// pending path.
	deadline := time.Now().Add(5 * time.Second)
	for {
		lease, acquired, err := tryAcquirePackagedUpdateHelperLease(packagedUpdateHelperTransitionLeasePath(staged))
		if err != nil {
			return nil, err
		}
		if acquired {
			return lease, nil
		}
		if time.Now().After(deadline) {
			return nil, errors.New("timed out acquiring packaged update helper handoff lease")
		}
		time.Sleep(time.Millisecond)
	}
}

func renamePackagedUpdateHelperState(source, destination string) error {
	deadline := time.Now().Add(time.Second)
	for {
		err := os.Rename(source, destination)
		if err == nil || os.IsNotExist(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(time.Millisecond)
	}
}

func cancelAuthorizedPackagedUpdateHelperHandoff(staged LocalStagedUpdate) error {
	for {
		if err := os.Rename(packagedUpdateHelperAuthorizedPath(staged.InstallPath), packagedUpdateHelperCancelledPath(staged.InstallPath)); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		outcome, err := readPackagedUpdateHelperOutcome(staged)
		if err == nil && outcome.State == packagedUpdateOutcomeCancelled {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func recoverAuthorizedParentExitFailure(cfg ExecutableUpdateHelperConfig, staged LocalStagedUpdate, cause error) error {
	result := cause
	if err := ensureBootableBinary(cfg.Current, cfg.Backup); err != nil {
		return errors.Join(result, fmt.Errorf("executable update recovery has no bootable executable: %w", err))
	}
	if err := cancelAuthorizedPackagedUpdateHelperHandoff(staged); err != nil {
		return errors.Join(result, fmt.Errorf("cancel packaged update helper after parent-exit failure: %w", err))
	}
	start := cfg.StartCommand
	if start == nil {
		start = packagedRestartCommand(cfg)
	}
	if _, err := start("exec", cfg.Current); err != nil {
		result = errors.Join(result, fmt.Errorf("executable update recovery restart failed: %w", err))
	}
	return result
}

func waitForPackagedUpdateHelperAuthorization(ctx context.Context, staged LocalStagedUpdate, timeout time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		outcome, err := readPackagedUpdateHelperOutcome(staged)
		if err == nil {
			switch outcome.State {
			case packagedUpdateOutcomeAuthorized:
				return nil
			case packagedUpdateOutcomePending:
			case packagedUpdateOutcomeCancelled:
				return errors.New("packaged update helper handoff was cancelled")
			default:
				return fmt.Errorf("packaged update helper authorization has invalid state %q", outcome.State)
			}
		} else if os.IsNotExist(err) {
			return errors.New("packaged update helper handoff was revoked")
		} else {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			cancelled, cancelErr := cancelPackagedUpdateHelperHandoff(staged)
			if cancelErr != nil {
				return errors.Join(fmt.Errorf("wait for packaged update helper authorization: %w", ctx.Err()), cancelErr)
			}
			if !cancelled {
				return nil
			}
			return fmt.Errorf("wait for packaged update helper authorization: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func writePackagedUpdateHelperOutcomeWithRetry(ctx context.Context, staged LocalStagedUpdate, state string) error {
	data, err := marshalPackagedUpdateHelperOutcome(staged, state)
	if err != nil {
		return err
	}
	return writePackagedUpdateHelperStateWithRetry(ctx, packagedUpdateHelperOutcomePath(staged.InstallPath), data)
}

func writePackagedUpdateHelperStateWithRetry(ctx context.Context, path string, data []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		err := atomicWriteState(path, data)
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

func writePackagedUpdateHelperRecoveryClaim(staged LocalStagedUpdate) error {
	data, err := marshalPackagedUpdateHelperOutcome(staged, packagedUpdateOutcomeRecovering)
	if err != nil {
		return err
	}
	return atomicWriteState(packagedUpdateHelperRecoveryClaimPath(staged.InstallPath), data)
}

func writePackagedUpdateHelperRecoveryClaimWithRetry(ctx context.Context, staged LocalStagedUpdate) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := writePackagedUpdateHelperRecoveryClaim(staged); err == nil {
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

func readPackagedUpdateHelperRecoveryClaim(staged LocalStagedUpdate) (packagedUpdateHelperOutcome, error) {
	return readPackagedUpdateHelperOutcomeAt(staged, packagedUpdateHelperRecoveryClaimPath(staged.InstallPath))
}

func removePackagedUpdateHelperOutcome(staged LocalStagedUpdate) error {
	var result error
	for _, path := range []string{
		packagedUpdateHelperOutcomePath(staged.InstallPath),
		packagedUpdateHelperPreparedPath(staged.InstallPath),
		packagedUpdateHelperAuthorizedPath(staged.InstallPath),
		packagedUpdateHelperCancelledPath(staged.InstallPath),
		packagedUpdateHelperRecoveryClaimPath(staged.InstallPath),
		packagedUpdateHelperRecoveryReadyPath(staged.InstallPath),
		packagedUpdateHelperTransitionLeasePath(staged),
	} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			result = errors.Join(result, err)
		}
	}
	return result
}

func readPackagedUpdateHelperOutcome(staged LocalStagedUpdate) (packagedUpdateHelperOutcome, error) {
	outcome, err := readPackagedUpdateHelperOutcomeAt(staged, packagedUpdateHelperOutcomePath(staged.InstallPath))
	if err == nil || !os.IsNotExist(err) {
		return outcome, err
	}
	outcome, err = readPackagedUpdateHelperOutcomeAt(staged, packagedUpdateHelperAuthorizedPath(staged.InstallPath))
	if err == nil {
		if outcome.State == packagedUpdateOutcomePending {
			outcome.State = packagedUpdateOutcomeAuthorized
		}
		return outcome, nil
	}
	if !os.IsNotExist(err) {
		return outcome, err
	}
	outcome, err = readPackagedUpdateHelperOutcomeAt(staged, packagedUpdateHelperCancelledPath(staged.InstallPath))
	if err == nil {
		outcome.State = packagedUpdateOutcomeCancelled
		return outcome, nil
	}
	if !os.IsNotExist(err) {
		return outcome, err
	}
	// Authorization may have atomically claimed the active pending identity
	// after the first authorized-path read. Recheck it before reporting that no
	// durable winner exists.
	outcome, authorizedErr := readPackagedUpdateHelperOutcomeAt(staged, packagedUpdateHelperAuthorizedPath(staged.InstallPath))
	if authorizedErr == nil {
		if outcome.State == packagedUpdateOutcomePending {
			outcome.State = packagedUpdateOutcomeAuthorized
		}
		return outcome, nil
	}
	return outcome, authorizedErr
}

func readPackagedUpdateHelperPrepared(staged LocalStagedUpdate) (packagedUpdateHelperOutcome, error) {
	return readPackagedUpdateHelperOutcomeAt(staged, packagedUpdateHelperPreparedPath(staged.InstallPath))
}

func readPackagedUpdateHelperOutcomeAt(staged LocalStagedUpdate, path string) (packagedUpdateHelperOutcome, error) {
	var outcome packagedUpdateHelperOutcome
	if staged.OutcomeID == "" || staged.PreviousVersion == "" || staged.Version == "" {
		return outcome, errors.New("packaged update helper outcome identity is incomplete")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return outcome, err
	}
	if err := json.Unmarshal(data, &outcome); err != nil {
		return outcome, err
	}
	if outcome.ID != staged.OutcomeID || outcome.PreviousVersion != staged.PreviousVersion || outcome.DesiredVersion != staged.Version {
		return packagedUpdateHelperOutcome{}, errors.New("packaged update helper outcome identity does not match the staged update")
	}
	switch outcome.State {
	case packagedUpdateOutcomePrepared, packagedUpdateOutcomePending, packagedUpdateOutcomeAuthorized,
		packagedUpdateOutcomeParentExited, packagedUpdateOutcomeBackupPublished, packagedUpdateOutcomeTargetPublished,
		packagedUpdateOutcomeValidating, packagedUpdateOutcomeRollingBack, packagedUpdateOutcomeRecovering,
		packagedUpdateOutcomeCancelled, packagedUpdateOutcomeSucceeded, packagedUpdateOutcomeRolledBack:
		return outcome, nil
	default:
		return packagedUpdateHelperOutcome{}, errors.New("packaged update helper outcome state is invalid")
	}
}

func RunExecutableUpdateHelper(ctx context.Context, cfg ExecutableUpdateHelperConfig) (runErr error) {
	traceExecutableUpdateHelperIntegration("started parent_pid=%d recovery=%t", cfg.ParentPID, cfg.Recovery)
	defer func() {
		traceExecutableUpdateHelperIntegration("finished err=%v", runErr)
	}()
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
		return errors.New("packaged update helper outcome identity is incomplete")
	}
	if cfg.WaitTimeout <= 0 {
		cfg.WaitTimeout = 30 * time.Second
	}
	if cfg.ValidationTimeout <= 0 {
		cfg.ValidationTimeout = 60 * time.Second
	}
	phase := packagedUpdateOutcomeAuthorized
	var lease *packagedUpdateHelperLease
	if outcomeEnabled {
		var acquired bool
		var err error
		lease, acquired, err = tryAcquirePackagedUpdateHelperLease(packagedUpdateHelperLeasePath(staged))
		if err != nil {
			return fmt.Errorf("acquire packaged update helper lease: %w", err)
		}
		if !acquired {
			return errors.New("packaged update helper lease is already owned")
		}
		traceExecutableUpdateHelperIntegration("acquired helper lease")
		defer lease.Close()
		_, recoveryClaimErr := readPackagedUpdateHelperRecoveryClaim(staged)
		switch {
		case recoveryClaimErr == nil && !cfg.Recovery:
			// Startup recovery durably claimed this exact operation while proving
			// the prior helper dead. A manager retry of the original helper must
			// exit without resuming replacement side effects.
			return nil
		case recoveryClaimErr == nil:
		case os.IsNotExist(recoveryClaimErr) && cfg.Recovery:
			return errors.New("executable update recovery helper ownership claim is missing")
		case os.IsNotExist(recoveryClaimErr):
		default:
			return fmt.Errorf("read executable update recovery ownership claim: %w", recoveryClaimErr)
		}
		if cfg.Recovery {
			return runExecutableUpdateRecoveryHelper(ctx, cfg, staged)
		}

		if _, err := readPackagedUpdateHelperPrepared(staged); err == nil {
			if err := claimPackagedUpdateHelperHandoff(ctx, staged); err != nil {
				return err
			}
			if err := waitForPackagedUpdateHelperAuthorization(ctx, staged, cfg.WaitTimeout); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		outcome, err := readPackagedUpdateHelperOutcome(staged)
		if err != nil {
			return err
		}
		if outcome.State == packagedUpdateOutcomePending {
			if err := waitForPackagedUpdateHelperAuthorization(ctx, staged, cfg.WaitTimeout); err != nil {
				return err
			}
			outcome, err = readPackagedUpdateHelperOutcome(staged)
			if err != nil {
				return err
			}
		}
		switch outcome.State {
		case packagedUpdateOutcomeCancelled, packagedUpdateOutcomeSucceeded, packagedUpdateOutcomeRolledBack:
			return nil
		case packagedUpdateOutcomeAuthorized, packagedUpdateOutcomeParentExited, packagedUpdateOutcomeBackupPublished,
			packagedUpdateOutcomeTargetPublished, packagedUpdateOutcomeValidating, packagedUpdateOutcomeRollingBack:
			phase = outcome.State
			traceExecutableUpdateHelperIntegration("resuming phase=%s", phase)
		default:
			return fmt.Errorf("packaged update helper cannot resume phase %q", outcome.State)
		}
	}

	if !outcomeEnabled || phase == packagedUpdateOutcomeAuthorized {
		traceExecutableUpdateHelperIntegration("waiting for parent exit")
		if err := waitForProcessExit(ctx, cfg.ParentPID, cfg.WaitTimeout); err != nil {
			if outcomeEnabled {
				return recoverAuthorizedParentExitFailure(cfg, staged, err)
			}
			return err
		}
		phase = packagedUpdateOutcomeParentExited
		traceExecutableUpdateHelperIntegration("parent exited")
		if outcomeEnabled {
			if err := writePackagedUpdateHelperPhaseWithRetry(ctx, staged, phase); err != nil {
				return fmt.Errorf("persist packaged update helper parent-exit phase: %w", err)
			}
		}
	}

	start := cfg.StartCommand
	if start == nil {
		start = packagedRestartCommand(cfg)
	}
	recoveryComplete := false
	replacementPublished := phase == packagedUpdateOutcomeTargetPublished || phase == packagedUpdateOutcomeValidating
	defer func() {
		if runErr == nil || recoveryComplete {
			return
		}
		if err := ensureBootableBinary(cfg.Current, cfg.Backup); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("executable update recovery has no bootable executable: %w", err))
			return
		}
		if _, err := start("exec", cfg.Current); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("executable update recovery restart failed: %w", err))
			return
		}
		if outcomeEnabled && !replacementPublished {
			if err := writePackagedUpdateHelperOutcomeWithRetry(ctx, staged, packagedUpdateOutcomeRolledBack); err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("persist binary rollback outcome: %w", err))
				return
			}
		}
		recoveryComplete = true
	}()

	if phase == packagedUpdateOutcomeRollingBack {
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
			if err := writePackagedUpdateHelperOutcomeWithRetry(ctx, staged, packagedUpdateOutcomeRolledBack); err != nil {
				return err
			}
		}
		recoveryComplete = true
		return errors.New("resumed binary rollback completed")
	}

	if phase == packagedUpdateOutcomeParentExited {
		traceExecutableUpdateHelperIntegration("publishing backup")
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
		phase = packagedUpdateOutcomeBackupPublished
		if outcomeEnabled {
			if err := writePackagedUpdateHelperPhaseWithRetry(ctx, staged, phase); err != nil {
				return fmt.Errorf("persist binary backup phase: %w", err)
			}
		}
	}
	if phase == packagedUpdateOutcomeBackupPublished {
		traceExecutableUpdateHelperIntegration("publishing replacement")
		if _, err := os.Stat(cfg.Staged); err == nil {
			if err := publishStagedBinary(cfg.Current, cfg.Staged); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		phase = packagedUpdateOutcomeTargetPublished
		replacementPublished = true
		if outcomeEnabled {
			if err := writePackagedUpdateHelperPhaseWithRetry(ctx, staged, phase); err != nil {
				return fmt.Errorf("persist binary target phase: %w", err)
			}
		}
	}
	if phase == packagedUpdateOutcomeTargetPublished {
		traceExecutableUpdateHelperIntegration("starting replacement validation")
		phase = packagedUpdateOutcomeValidating
		if outcomeEnabled {
			if err := writePackagedUpdateHelperPhaseWithRetry(ctx, staged, phase); err != nil {
				return fmt.Errorf("persist binary validation phase: %w", err)
			}
		}
	}

	stopSuccessor, restartErr := start("exec", cfg.Current)
	successorStarted := restartErr == nil
	traceExecutableUpdateHelperIntegration("replacement start succeeded=%t err=%v", successorStarted, restartErr)
	if successorStarted {
		validationCtx, cancel := context.WithTimeout(ctx, cfg.ValidationTimeout)
		restartErr = waitForExpectedHealth(validationCtx, cfg.HealthURL, cfg.ExpectedVersion, cfg.HealthClient)
		cancel()
	}
	if restartErr == nil {
		traceExecutableUpdateHelperIntegration("replacement health validation succeeded")
		if outcomeEnabled {
			if err := writePackagedUpdateHelperOutcomeWithRetry(ctx, staged, packagedUpdateOutcomeSucceeded); err != nil {
				return fmt.Errorf("persist binary success outcome: %w", err)
			}
		}
		return nil
	}
	err := restartErr
	traceExecutableUpdateHelperIntegration("replacement health validation failed err=%v", err)
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
		if phaseErr := writePackagedUpdateHelperPhaseWithRetry(ctx, staged, packagedUpdateOutcomeRollingBack); phaseErr != nil {
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
		if outcomeErr := writePackagedUpdateHelperOutcomeWithRetry(ctx, staged, packagedUpdateOutcomeRolledBack); outcomeErr != nil {
			return fmt.Errorf("validation failed: %v; persist binary rollback outcome: %w", err, outcomeErr)
		}
	}
	recoveryComplete = true
	return fmt.Errorf("new binary validation failed and prior binary was restored: %w", err)
}

func traceExecutableUpdateHelperIntegration(format string, args ...any) {
	if os.Getenv(updateIntegrationHelperLogEnv) == "" {
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "[update-helper] "+format+"\n", args...)
}

func runExecutableUpdateRecoveryHelper(ctx context.Context, cfg ExecutableUpdateHelperConfig, staged LocalStagedUpdate) error {
	outcome, err := readPackagedUpdateHelperOutcome(staged)
	if err != nil {
		return fmt.Errorf("read executable update recovery phase: %w", err)
	}
	switch outcome.State {
	case packagedUpdateOutcomeCancelled, packagedUpdateOutcomeSucceeded, packagedUpdateOutcomeRolledBack:
		return nil
	case packagedUpdateOutcomeAuthorized, packagedUpdateOutcomeParentExited, packagedUpdateOutcomeBackupPublished,
		packagedUpdateOutcomeTargetPublished, packagedUpdateOutcomeValidating, packagedUpdateOutcomeRollingBack:
	default:
		return fmt.Errorf("executable update recovery has invalid phase %q", outcome.State)
	}
	ready, err := marshalPackagedUpdateHelperOutcome(staged, packagedUpdateOutcomeRecovering)
	if err != nil {
		return err
	}
	if err := atomicWriteState(packagedUpdateHelperRecoveryReadyPath(staged.InstallPath), ready); err != nil {
		return fmt.Errorf("publish executable update recovery readiness: %w", err)
	}
	if err := waitForProcessExit(ctx, cfg.ParentPID, cfg.WaitTimeout); err != nil {
		return err
	}
	start := cfg.StartCommand
	if start == nil {
		start = packagedRestartCommand(cfg)
	}
	if outcome.State == packagedUpdateOutcomeRollingBack {
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
		return writePackagedUpdateHelperOutcomeWithRetry(ctx, staged, packagedUpdateOutcomeRolledBack)
	}
	if cfg.RunningVersion == staged.PreviousVersion {
		if _, err := os.Stat(staged.ArtifactPath); err == nil {
			if err := ensureBootableBinary(cfg.Current, cfg.Backup); err != nil {
				return err
			}
			if _, err := start("exec", cfg.Current); err != nil {
				return err
			}
			return writePackagedUpdateHelperOutcomeWithRetry(ctx, staged, packagedUpdateOutcomeCancelled)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return recoverPublishedExecutableTarget(ctx, cfg, staged, start)
}

func recoverPublishedExecutableTarget(ctx context.Context, cfg ExecutableUpdateHelperConfig, staged LocalStagedUpdate, start func(string, string) (func(context.Context) error, error)) error {
	stopSuccessor, restartErr := start("exec", cfg.Current)
	if restartErr == nil {
		validationCtx, cancel := context.WithTimeout(ctx, cfg.ValidationTimeout)
		restartErr = waitForExpectedHealth(validationCtx, cfg.HealthURL, cfg.ExpectedVersion, cfg.HealthClient)
		cancel()
	}
	if restartErr == nil {
		return writePackagedUpdateHelperOutcomeWithRetry(ctx, staged, packagedUpdateOutcomeSucceeded)
	}
	if stopSuccessor != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), cfg.WaitTimeout)
		stopErr := stopSuccessor(stopCtx)
		cancel()
		if stopErr != nil {
			return errors.Join(restartErr, fmt.Errorf("stopping failed recovery successor: %w", stopErr))
		}
	}
	if err := writePackagedUpdateHelperPhaseWithRetry(ctx, staged, packagedUpdateOutcomeRollingBack); err != nil {
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
	if err := writePackagedUpdateHelperOutcomeWithRetry(ctx, staged, packagedUpdateOutcomeRolledBack); err != nil {
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

func packagedRestartCommand(cfg ExecutableUpdateHelperConfig) func(string, string) (func(context.Context) error, error) {
	return func(_, _ string) (func(context.Context) error, error) {
		arguments := cfg.Arguments
		if len(arguments) == 0 {
			arguments = []string{cfg.Current}
		}
		cmd := exec.Command(cfg.Current, arguments[1:]...)
		cmd.Args[0] = arguments[0]
		cmd.Dir = cfg.WorkingDirectory
		if health, err := url.Parse(cfg.HealthURL); err == nil && health.Port() != "" {
			cmd.Env = append(os.Environ(), "PORT="+health.Port())
		}
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

type packagedUpdateRelaunchMetadata struct {
	Arguments          []string `json:"arguments"`
	WorkingDirectory   string   `json:"working_directory"`
	ExecutableRelative string   `json:"executable_relative,omitempty"`
}

func LoadExecutableUpdateHelperRelaunch(reader io.Reader, cfg *ExecutableUpdateHelperConfig) error {
	if reader == nil || cfg == nil {
		return errors.New("executable update helper relaunch metadata is unavailable")
	}
	var metadata packagedUpdateRelaunchMetadata
	decoder := json.NewDecoder(io.LimitReader(reader, 1024*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return fmt.Errorf("decode executable update helper relaunch metadata: %w", err)
	}
	if len(metadata.Arguments) == 0 || metadata.WorkingDirectory == "" || !filepath.IsAbs(metadata.WorkingDirectory) {
		return errors.New("executable update helper relaunch metadata is incomplete")
	}
	cfg.Arguments = append([]string(nil), metadata.Arguments...)
	cfg.WorkingDirectory = metadata.WorkingDirectory
	return nil
}

func LoadExecutableUpdateHelperRelaunchFile(path string, cfg *ExecutableUpdateHelperConfig) error {
	if path == "" || cfg == nil {
		return errors.New("executable update helper relaunch metadata path is unavailable")
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open executable update helper relaunch metadata: %w", err)
	}
	defer os.Remove(path)
	defer file.Close()
	return LoadExecutableUpdateHelperRelaunch(file, cfg)
}

func ParseExecutableUpdateHelperArgs(args []string) (ExecutableUpdateHelperConfig, error) {
	allowed := map[string]bool{
		"--parent-pid": true, "--current": true, "--staged": true, "--backup": true,
		"--health-url": true, "--expected-version": true, "--previous-version": true,
		"--outcome-id": true, "--running-version": true, "--recovery": true,
		"--relaunch-metadata": true,
	}
	values := map[string]string{}
	for len(args) > 0 {
		if len(args) < 2 || len(args[0]) < 3 || args[0][:2] != "--" {
			return ExecutableUpdateHelperConfig{}, errors.New("invalid executable-update-helper arguments")
		}
		if !allowed[args[0]] {
			return ExecutableUpdateHelperConfig{}, fmt.Errorf("unsupported executable-update-helper argument %s", args[0])
		}
		if _, exists := values[args[0]]; exists {
			return ExecutableUpdateHelperConfig{}, errors.New("duplicate executable-update-helper argument")
		}
		values[args[0]] = args[1]
		args = args[2:]
	}
	pid, err := strconv.Atoi(values["--parent-pid"])
	if err != nil {
		return ExecutableUpdateHelperConfig{}, errors.New("invalid parent PID")
	}
	cfg := ExecutableUpdateHelperConfig{ParentPID: pid, Current: values["--current"], Staged: values["--staged"], Backup: values["--backup"], HealthURL: values["--health-url"], ExpectedVersion: values["--expected-version"], PreviousVersion: values["--previous-version"], OutcomeID: values["--outcome-id"], RunningVersion: values["--running-version"], RelaunchMetadataPath: values["--relaunch-metadata"], Recovery: values["--recovery"] == "true"}
	if values["--recovery"] != "" && values["--recovery"] != "true" {
		return ExecutableUpdateHelperConfig{}, errors.New("invalid recovery mode")
	}
	if cfg.Recovery && cfg.RunningVersion == "" {
		return ExecutableUpdateHelperConfig{}, errors.New("executable update recovery running version is required")
	}
	if cfg.PreviousVersion == "" || cfg.OutcomeID == "" {
		return ExecutableUpdateHelperConfig{}, errors.New("executable update helper outcome identity is required")
	}
	for _, path := range []string{cfg.Current, cfg.Staged, cfg.Backup} {
		if !filepath.IsAbs(path) {
			return ExecutableUpdateHelperConfig{}, errors.New("executable-update-helper paths must be absolute")
		}
	}
	if cfg.RelaunchMetadataPath != "" && !filepath.IsAbs(cfg.RelaunchMetadataPath) {
		return ExecutableUpdateHelperConfig{}, errors.New("executable-update-helper relaunch metadata path must be absolute")
	}
	return cfg, nil
}
