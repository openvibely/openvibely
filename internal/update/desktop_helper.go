package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type DesktopHelperConfig struct {
	ParentPID                      int
	Current, Staged, Backup        string
	HealthURL, ExpectedVersion     string
	PreviousVersion, OutcomeID     string
	RunningVersion                 string
	ExecutableRelative             string
	Recovery                       bool
	Arguments                      []string
	WorkingDirectory               string
	WaitTimeout, ValidationTimeout time.Duration
	StartCommand                   func() (func(context.Context) error, error)
	HealthClient                   *http.Client
}

func validateDesktopHelperPaths(staged LocalStagedUpdate) error {
	if !filepath.IsAbs(staged.InstallPath) || staged.ArtifactPath != staged.InstallPath+".openvibely-new" || staged.BackupPath != staged.InstallPath+".openvibely-backup" {
		return errors.New("desktop helper paths must use validated absolute sibling names")
	}
	return nil
}

func desktopSuccessorCommand(cfg DesktopHelperConfig) func() (func(context.Context) error, error) {
	return func() (func(context.Context) error, error) {
		health, err := url.Parse(cfg.HealthURL)
		if err != nil || health.Port() == "" {
			return nil, errors.New("desktop health URL has no relaunch port")
		}
		portEnv := "PORT=" + health.Port()
		executable := cfg.Current
		if cfg.ExecutableRelative != "" {
			executable = filepath.Join(cfg.Current, filepath.FromSlash(cfg.ExecutableRelative))
		}
		arguments := cfg.Arguments
		if len(arguments) == 0 {
			arguments = []string{executable}
		}
		if runtime.GOOS == "darwin" && filepath.Ext(cfg.Current) == ".app" {
			openArgs := []string{"-n", cfg.Current, "--env", portEnv}
			for _, env := range inheritedDarwinDesktopRelaunchEnvironment() {
				openArgs = append(openArgs, "--env", env)
			}
			if len(arguments) > 1 {
				openArgs = append(openArgs, "--args")
				openArgs = append(openArgs, arguments[1:]...)
			}
			cmd := exec.Command("open", openArgs...)
			cmd.Dir = cfg.WorkingDirectory
			if err := cmd.Start(); err != nil {
				return nil, err
			}
			if err := cmd.Wait(); err != nil {
				return nil, err
			}
			return func(context.Context) error { return stopDarwinAppBundleProcess(executable) }, nil
		}
		cmd := exec.Command(executable, arguments[1:]...)
		cmd.Args[0] = arguments[0]
		cmd.Dir = cfg.WorkingDirectory
		cmd.Env = append(os.Environ(), portEnv)
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return func(context.Context) error { return stopStartedProcess(cmd) }, nil
	}
}

func inheritedDarwinDesktopRelaunchEnvironment() []string {
	var inherited []string
	for _, env := range os.Environ() {
		key, _, ok := strings.Cut(env, "=")
		if !ok {
			continue
		}
		if key == "HOME" || strings.HasPrefix(key, "OPENVIBELY_") || key == "DATABASE_PATH" || key == "PROJECT_REPO_ROOT" || key == "DISABLE_UPDATE_NOTIFICATIONS" || key == "ENVIRONMENT" || key == "AUTH_ENABLED" {
			inherited = append(inherited, env)
		}
	}
	return inherited
}

func stopDarwinAppBundleProcess(executable string) error {
	output, err := exec.Command("ps", "-axo", "pid=,args=").Output()
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(output), "\n") {
		trimmed := strings.TrimSpace(line)
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid == os.Getpid() {
			continue
		}
		commandLine := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
		if commandLine != executable && !strings.HasPrefix(commandLine, executable+" ") {
			continue
		}
		process, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
	}
	return nil
}

func rollbackDesktopInstallUnit(staged LocalStagedUpdate) error {
	if runtime.GOOS != "windows" {
		return atomicExchangeInstallUnits(staged.InstallPath, staged.ArtifactPath)
	}
	_ = os.RemoveAll(staged.ArtifactPath)
	if err := copyFilesystemPath(staged.BackupPath, staged.ArtifactPath); err != nil {
		return err
	}
	return atomicExchangeInstallUnits(staged.InstallPath, staged.ArtifactPath)
}

func RunDesktopHelper(ctx context.Context, cfg DesktopHelperConfig) error {
	staged := LocalStagedUpdate{ArtifactPath: cfg.Staged, InstallPath: cfg.Current, BackupPath: cfg.Backup, Version: cfg.ExpectedVersion, PreviousVersion: cfg.PreviousVersion, OutcomeID: cfg.OutcomeID}
	if err := validateDesktopHelperPaths(staged); err != nil {
		return err
	}
	if cfg.ParentPID <= 1 || cfg.ParentPID == os.Getpid() || cfg.HealthURL == "" || cfg.ExpectedVersion == "" || cfg.PreviousVersion == "" || cfg.OutcomeID == "" {
		return errors.New("desktop helper configuration is incomplete")
	}
	if cfg.WaitTimeout <= 0 {
		cfg.WaitTimeout = 30 * time.Second
	}
	if cfg.ValidationTimeout <= 0 {
		cfg.ValidationTimeout = 60 * time.Second
	}
	lease, acquired, err := tryAcquireBinaryHelperLease(binaryHelperLeasePath(staged))
	if err != nil {
		return err
	}
	if !acquired {
		return errors.New("desktop helper lease is already owned")
	}
	defer lease.Close()

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
	if cfg.Recovery {
		ready, err := marshalBinaryHelperOutcome(staged, binaryOutcomeRecovering)
		if err != nil {
			return err
		}
		if err := atomicWriteState(binaryHelperRecoveryReadyPath(staged.InstallPath), ready); err != nil {
			return err
		}
	}
	switch outcome.State {
	case binaryOutcomeCancelled, binaryOutcomeSucceeded, binaryOutcomeRolledBack:
		return nil
	case binaryOutcomeAuthorized:
		if err := waitForProcessExit(ctx, cfg.ParentPID, cfg.WaitTimeout); err != nil {
			if cancelErr := cancelAuthorizedBinaryHelperHandoff(staged); cancelErr != nil {
				return errors.Join(err, cancelErr)
			}
			start := cfg.StartCommand
			if start == nil {
				start = desktopSuccessorCommand(cfg)
			}
			if _, startErr := start(); startErr != nil {
				return errors.Join(err, startErr)
			}
			return err
		}
		if err := writeBinaryHelperPhaseWithRetry(ctx, staged, binaryOutcomeParentExited); err != nil {
			return err
		}
		outcome.State = binaryOutcomeParentExited
	}

	if outcome.State == binaryOutcomeParentExited {
		if cfg.Recovery && cfg.RunningVersion == staged.Version {
			if err := writeBinaryHelperPhaseWithRetry(ctx, staged, binaryOutcomeTargetPublished); err != nil {
				return err
			}
			outcome.State = binaryOutcomeTargetPublished
		} else {
			if err := atomicExchangeInstallUnits(staged.InstallPath, staged.ArtifactPath); err != nil {
				return err
			}
			if err := syncDirectory(filepath.Dir(staged.InstallPath)); err != nil {
				return err
			}
			if err := writeBinaryHelperPhaseWithRetry(ctx, staged, binaryOutcomeTargetPublished); err != nil {
				return err
			}
			outcome.State = binaryOutcomeTargetPublished
		}
	}
	if outcome.State == binaryOutcomeTargetPublished {
		if err := writeBinaryHelperPhaseWithRetry(ctx, staged, binaryOutcomeValidating); err != nil {
			return err
		}
		outcome.State = binaryOutcomeValidating
	}
	if outcome.State == binaryOutcomeRollingBack {
		if !(cfg.Recovery && cfg.RunningVersion == staged.PreviousVersion) {
			if err := rollbackDesktopInstallUnit(staged); err != nil {
				return err
			}
		}
		start := cfg.StartCommand
		if start == nil {
			start = desktopSuccessorCommand(cfg)
		}
		if _, err := start(); err != nil {
			return err
		}
		return writeBinaryHelperOutcomeWithRetry(ctx, staged, binaryOutcomeRolledBack)
	}
	if outcome.State != binaryOutcomeValidating {
		return fmt.Errorf("desktop helper cannot resume phase %q", outcome.State)
	}

	start := cfg.StartCommand
	if start == nil {
		start = desktopSuccessorCommand(cfg)
	}
	stop, startErr := start()
	if startErr == nil {
		validationCtx, cancel := context.WithTimeout(ctx, cfg.ValidationTimeout)
		startErr = waitForExpectedHealth(validationCtx, cfg.HealthURL, cfg.ExpectedVersion, cfg.HealthClient)
		cancel()
	}
	if startErr == nil {
		if err := writeBinaryHelperOutcomeWithRetry(ctx, staged, binaryOutcomeSucceeded); err != nil {
			return err
		}
		_ = os.RemoveAll(staged.ArtifactPath)
		return nil
	}
	if stop != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), cfg.WaitTimeout)
		stopErr := stop(stopCtx)
		cancel()
		if stopErr != nil {
			return errors.Join(startErr, fmt.Errorf("stop failed desktop successor: %w", stopErr))
		}
	}
	if err := writeBinaryHelperPhaseWithRetry(ctx, staged, binaryOutcomeRollingBack); err != nil {
		return errors.Join(startErr, err)
	}
	if err := rollbackDesktopInstallUnit(staged); err != nil {
		return errors.Join(startErr, err)
	}
	if _, err := start(); err != nil {
		return errors.Join(startErr, fmt.Errorf("restart rolled-back desktop: %w", err))
	}
	if err := writeBinaryHelperOutcomeWithRetry(ctx, staged, binaryOutcomeRolledBack); err != nil {
		return errors.Join(startErr, err)
	}
	return fmt.Errorf("desktop validation failed and predecessor was restored: %w", startErr)
}

func LoadDesktopHelperRelaunch(reader io.Reader, cfg *DesktopHelperConfig) error {
	if reader == nil || cfg == nil {
		return errors.New("desktop helper relaunch metadata is unavailable")
	}
	var metadata binaryRelaunchMetadata
	decoder := json.NewDecoder(io.LimitReader(reader, 1024*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return err
	}
	if len(metadata.Arguments) == 0 || !filepath.IsAbs(metadata.WorkingDirectory) {
		return errors.New("desktop helper relaunch metadata is incomplete")
	}
	cfg.Arguments = append([]string(nil), metadata.Arguments...)
	cfg.WorkingDirectory = metadata.WorkingDirectory
	cfg.ExecutableRelative = metadata.ExecutableRelative
	return nil
}

func ParseDesktopHelperArgs(args []string) (DesktopHelperConfig, error) {
	allowed := map[string]bool{"--parent-pid": true, "--current": true, "--staged": true, "--backup": true, "--health-url": true, "--expected-version": true, "--previous-version": true, "--outcome-id": true, "--recovery": true, "--running-version": true}
	values := map[string]string{}
	for len(args) > 0 {
		if len(args) < 2 || !allowed[args[0]] || values[args[0]] != "" {
			return DesktopHelperConfig{}, errors.New("invalid desktop-update-helper arguments")
		}
		values[args[0]], args = args[1], args[2:]
	}
	pid, err := strconv.Atoi(values["--parent-pid"])
	if err != nil {
		return DesktopHelperConfig{}, errors.New("invalid parent PID")
	}
	if values["--recovery"] != "" && values["--recovery"] != "true" {
		return DesktopHelperConfig{}, errors.New("invalid desktop helper recovery mode")
	}
	if values["--recovery"] == "true" && values["--running-version"] == "" {
		return DesktopHelperConfig{}, errors.New("desktop helper recovery running version is required")
	}
	return DesktopHelperConfig{ParentPID: pid, Current: values["--current"], Staged: values["--staged"], Backup: values["--backup"], HealthURL: values["--health-url"], ExpectedVersion: values["--expected-version"], PreviousVersion: values["--previous-version"], OutcomeID: values["--outcome-id"], Recovery: values["--recovery"] == "true", RunningVersion: values["--running-version"]}, nil
}
