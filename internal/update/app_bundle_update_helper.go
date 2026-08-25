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

type AppBundleUpdateHelperConfig struct {
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

func validateAppBundleUpdateHelperPaths(staged LocalStagedUpdate) error {
	if !filepath.IsAbs(staged.InstallPath) || staged.ArtifactPath != staged.InstallPath+".openvibely-new" || staged.BackupPath != staged.InstallPath+".openvibely-backup" {
		return errors.New("app-bundle update helper paths must use validated absolute sibling names")
	}
	return nil
}

func appBundleSuccessorCommand(cfg AppBundleUpdateHelperConfig) func() (func(context.Context) error, error) {
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
			return func(ctx context.Context) error {
				return stopDarwinAppBundleProcess(ctx, executable, cfg.HealthURL, cfg.HealthClient)
			}, nil
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

func stopDarwinAppBundleProcess(ctx context.Context, executable, healthURL string, healthClient *http.Client) error {
	executablePaths := []string{executable}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil && resolved != executable {
		executablePaths = append(executablePaths, resolved)
	}
	output, err := exec.Command("ps", "-ww", "-axo", "pid=,comm=").Output()
	if err != nil {
		return err
	}
	var stoppedPIDs []int
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
		processExecutable := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
		matches := false
		for _, candidate := range executablePaths {
			if processExecutable == candidate {
				matches = true
				break
			}
		}
		if !matches {
			if resolved, resolveErr := filepath.EvalSymlinks(processExecutable); resolveErr == nil {
				matches = resolved == executablePaths[len(executablePaths)-1]
			}
		}
		if !matches {
			continue
		}
		process, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		stoppedPIDs = append(stoppedPIDs, pid)
	}
	for _, pid := range stoppedPIDs {
		if err := waitForProcessExit(ctx, pid, 30*time.Second); err != nil {
			return fmt.Errorf("wait for failed desktop successor %d: %w", pid, err)
		}
	}
	return waitForHealthEndpointExit(ctx, healthURL, healthClient)
}

func waitForHealthEndpointExit(ctx context.Context, healthURL string, healthClient *http.Client) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if healthClient == nil {
		healthClient = http.DefaultClient
	}
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		if err != nil {
			return err
		}
		response, err := healthClient.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			return nil
		}
		if response.Body != nil {
			_ = response.Body.Close()
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func rollbackAppBundleInstallUnit(staged LocalStagedUpdate) error {
	if runtime.GOOS != "windows" {
		return atomicExchangeInstallUnits(staged.InstallPath, staged.ArtifactPath)
	}
	_ = os.RemoveAll(staged.ArtifactPath)
	if err := copyFilesystemPath(staged.BackupPath, staged.ArtifactPath); err != nil {
		return err
	}
	return atomicExchangeInstallUnits(staged.InstallPath, staged.ArtifactPath)
}

func RunAppBundleUpdateHelper(ctx context.Context, cfg AppBundleUpdateHelperConfig) error {
	staged := LocalStagedUpdate{ArtifactPath: cfg.Staged, InstallPath: cfg.Current, BackupPath: cfg.Backup, Version: cfg.ExpectedVersion, PreviousVersion: cfg.PreviousVersion, OutcomeID: cfg.OutcomeID}
	if err := validateAppBundleUpdateHelperPaths(staged); err != nil {
		return err
	}
	if cfg.ParentPID <= 1 || cfg.ParentPID == os.Getpid() || cfg.HealthURL == "" || cfg.ExpectedVersion == "" || cfg.PreviousVersion == "" || cfg.OutcomeID == "" {
		return errors.New("app-bundle update helper configuration is incomplete")
	}
	if cfg.WaitTimeout <= 0 {
		cfg.WaitTimeout = 30 * time.Second
	}
	if cfg.ValidationTimeout <= 0 {
		cfg.ValidationTimeout = 60 * time.Second
	}
	lease, acquired, err := tryAcquirePackagedUpdateHelperLease(packagedUpdateHelperLeasePath(staged))
	if err != nil {
		return err
	}
	if !acquired {
		return errors.New("app-bundle update helper lease is already owned")
	}
	defer lease.Close()

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
	parentExited := false
	if cfg.Recovery {
		ready, err := marshalPackagedUpdateHelperOutcome(staged, packagedUpdateOutcomeRecovering)
		if err != nil {
			return err
		}
		if err := atomicWriteState(packagedUpdateHelperRecoveryReadyPath(staged.InstallPath), ready); err != nil {
			return err
		}
		if err := waitForProcessExit(ctx, cfg.ParentPID, cfg.WaitTimeout); err != nil {
			return err
		}
		parentExited = true
	}
	switch outcome.State {
	case packagedUpdateOutcomeCancelled, packagedUpdateOutcomeSucceeded, packagedUpdateOutcomeRolledBack:
		return nil
	case packagedUpdateOutcomeAuthorized:
		if !parentExited {
			if err := waitForProcessExit(ctx, cfg.ParentPID, cfg.WaitTimeout); err != nil {
				if cancelErr := cancelAuthorizedPackagedUpdateHelperHandoff(staged); cancelErr != nil {
					return errors.Join(err, cancelErr)
				}
				start := cfg.StartCommand
				if start == nil {
					start = appBundleSuccessorCommand(cfg)
				}
				if _, startErr := start(); startErr != nil {
					return errors.Join(err, startErr)
				}
				return err
			}
		}
		if err := writePackagedUpdateHelperPhaseWithRetry(ctx, staged, packagedUpdateOutcomeParentExited); err != nil {
			return err
		}
		outcome.State = packagedUpdateOutcomeParentExited
	}

	if outcome.State == packagedUpdateOutcomeParentExited {
		if cfg.Recovery && cfg.RunningVersion == staged.Version {
			if err := writePackagedUpdateHelperPhaseWithRetry(ctx, staged, packagedUpdateOutcomeTargetPublished); err != nil {
				return err
			}
			outcome.State = packagedUpdateOutcomeTargetPublished
		} else {
			if err := atomicExchangeInstallUnits(staged.InstallPath, staged.ArtifactPath); err != nil {
				return err
			}
			if err := syncDirectory(filepath.Dir(staged.InstallPath)); err != nil {
				return err
			}
			if err := writePackagedUpdateHelperPhaseWithRetry(ctx, staged, packagedUpdateOutcomeTargetPublished); err != nil {
				return err
			}
			outcome.State = packagedUpdateOutcomeTargetPublished
		}
	}
	if outcome.State == packagedUpdateOutcomeTargetPublished {
		if err := writePackagedUpdateHelperPhaseWithRetry(ctx, staged, packagedUpdateOutcomeValidating); err != nil {
			return err
		}
		outcome.State = packagedUpdateOutcomeValidating
	}
	if outcome.State == packagedUpdateOutcomeRollingBack {
		if !(cfg.Recovery && cfg.RunningVersion == staged.PreviousVersion) {
			if err := rollbackAppBundleInstallUnit(staged); err != nil {
				return err
			}
		}
		start := cfg.StartCommand
		if start == nil {
			start = appBundleSuccessorCommand(cfg)
		}
		if _, err := start(); err != nil {
			return err
		}
		return writePackagedUpdateHelperOutcomeWithRetry(ctx, staged, packagedUpdateOutcomeRolledBack)
	}
	if outcome.State != packagedUpdateOutcomeValidating {
		return fmt.Errorf("app-bundle update helper cannot resume phase %q", outcome.State)
	}

	start := cfg.StartCommand
	if start == nil {
		start = appBundleSuccessorCommand(cfg)
	}
	stop, startErr := start()
	if startErr == nil {
		validationCtx, cancel := context.WithTimeout(ctx, cfg.ValidationTimeout)
		startErr = waitForExpectedHealth(validationCtx, cfg.HealthURL, cfg.ExpectedVersion, cfg.HealthClient)
		cancel()
	}
	if startErr == nil {
		if err := writePackagedUpdateHelperOutcomeWithRetry(ctx, staged, packagedUpdateOutcomeSucceeded); err != nil {
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
			return errors.Join(startErr, fmt.Errorf("stop failed app-bundle successor: %w", stopErr))
		}
	}
	if err := writePackagedUpdateHelperPhaseWithRetry(ctx, staged, packagedUpdateOutcomeRollingBack); err != nil {
		return errors.Join(startErr, err)
	}
	if err := rollbackAppBundleInstallUnit(staged); err != nil {
		return errors.Join(startErr, err)
	}
	if _, err := start(); err != nil {
		return errors.Join(startErr, fmt.Errorf("restart rolled-back app bundle: %w", err))
	}
	if err := writePackagedUpdateHelperOutcomeWithRetry(ctx, staged, packagedUpdateOutcomeRolledBack); err != nil {
		return errors.Join(startErr, err)
	}
	return fmt.Errorf("app-bundle validation failed and predecessor was restored: %w", startErr)
}

func LoadAppBundleUpdateHelperRelaunch(reader io.Reader, cfg *AppBundleUpdateHelperConfig) error {
	if reader == nil || cfg == nil {
		return errors.New("app-bundle update helper relaunch metadata is unavailable")
	}
	var metadata packagedUpdateRelaunchMetadata
	decoder := json.NewDecoder(io.LimitReader(reader, 1024*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return err
	}
	if len(metadata.Arguments) == 0 || !filepath.IsAbs(metadata.WorkingDirectory) {
		return errors.New("app-bundle update helper relaunch metadata is incomplete")
	}
	cfg.Arguments = append([]string(nil), metadata.Arguments...)
	cfg.WorkingDirectory = metadata.WorkingDirectory
	cfg.ExecutableRelative = metadata.ExecutableRelative
	return nil
}

func ParseAppBundleUpdateHelperArgs(args []string) (AppBundleUpdateHelperConfig, error) {
	allowed := map[string]bool{"--parent-pid": true, "--current": true, "--staged": true, "--backup": true, "--health-url": true, "--expected-version": true, "--previous-version": true, "--outcome-id": true, "--recovery": true, "--running-version": true}
	values := map[string]string{}
	for len(args) > 0 {
		if len(args) < 2 || !allowed[args[0]] || values[args[0]] != "" {
			return AppBundleUpdateHelperConfig{}, errors.New("invalid app-bundle-update-helper arguments")
		}
		values[args[0]], args = args[1], args[2:]
	}
	pid, err := strconv.Atoi(values["--parent-pid"])
	if err != nil {
		return AppBundleUpdateHelperConfig{}, errors.New("invalid parent PID")
	}
	if values["--recovery"] != "" && values["--recovery"] != "true" {
		return AppBundleUpdateHelperConfig{}, errors.New("invalid app-bundle update helper recovery mode")
	}
	if values["--recovery"] == "true" && values["--running-version"] == "" {
		return AppBundleUpdateHelperConfig{}, errors.New("app-bundle update helper recovery running version is required")
	}
	return AppBundleUpdateHelperConfig{ParentPID: pid, Current: values["--current"], Staged: values["--staged"], Backup: values["--backup"], HealthURL: values["--health-url"], ExpectedVersion: values["--expected-version"], PreviousVersion: values["--previous-version"], OutcomeID: values["--outcome-id"], Recovery: values["--recovery"] == "true", RunningVersion: values["--running-version"]}, nil
}
