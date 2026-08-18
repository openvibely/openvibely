package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	wailsupdater "github.com/wailsapp/wails/v3/pkg/updater"
)

type LocalStagedUpdate struct {
	ArtifactPath    string `json:"artifact_path"`
	InstallPath     string `json:"install_path"`
	BackupPath      string `json:"backup_path"`
	Version         string `json:"version"`
	PreviousVersion string `json:"previous_version,omitempty"`
	OutcomeID       string `json:"outcome_id,omitempty"`
}

func packagedUpdateHelperPath(installPath string) string {
	path := installPath + ".openvibely-helper"
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	return path
}

type WailsProvider struct {
	Client  *Client
	Current CurrentBuild
	Release *VerifiedRelease
}

func (p *WailsProvider) Name() string { return "openvibely" }
func (p *WailsProvider) Check(context.Context, wailsupdater.CheckRequest) (*wailsupdater.Release, error) {
	if p.Release == nil {
		return nil, nil
	}
	if err := p.Client.ValidateForInstall(*p.Release, p.Current); err != nil {
		return nil, err
	}
	digest, err := hex.DecodeString(p.Release.Target.SHA256)
	if err != nil {
		return nil, err
	}
	return &wailsupdater.Release{
		Version:      p.Release.Metadata.Version,
		Channel:      p.Release.Metadata.Channel,
		Notes:        p.Release.Metadata.ReleaseNotesURL,
		PublishedAt:  p.Release.Metadata.PublishedAt,
		Artifact:     wailsupdater.Artifact{Filename: p.Release.Target.Filename, Filetype: p.Release.Target.Filetype, Size: p.Release.Target.Size, Platform: p.Release.Target.OS, Arch: p.Release.Target.Arch},
		Verification: &wailsupdater.Verification{DigestAlgo: "sha256", Digest: digest},
	}, nil
}
func (p *WailsProvider) Download(ctx context.Context, _ *wailsupdater.Release, dst io.Writer, progress func(int64, int64)) error {
	if p.Release == nil {
		return errors.New("no verified desktop release")
	}
	return p.Client.Download(ctx, *p.Release, dst, progress)
}

type WailsInstaller struct {
	Updater            *wailsupdater.Updater
	Provider           *WailsProvider
	AppPath            string
	ProtectedDataPaths []string
	BackupPath         string
	HealthURL          string
	Arguments          []string
	WorkingDirectory   string
	Shutdown           func()
	StartHelper        func(*exec.Cmd) error
	awaitHelperHandoff func(context.Context, LocalStagedUpdate) error
	Relaunch           func(string) error
	initOnce           sync.Once
	initErr            error
}

func (i *WailsInstaller) Stage(ctx context.Context, release VerifiedRelease) (any, error) {
	appPath := i.AppPath
	if appPath == "" {
		var err error
		appPath, err = currentApplicationBundle()
		if err != nil {
			return nil, err
		}
	}
	backupPath := i.BackupPath
	if backupPath == "" {
		backupPath = appPath + ".openvibely-backup"
	}
	staged := LocalStagedUpdate{InstallPath: appPath, BackupPath: backupPath, Version: release.Metadata.Version, PreviousVersion: i.Provider.Current.Version, OutcomeID: randomGeneration()}
	if err := validateDesktopDataBoundaries(staged, i.ProtectedDataPaths); err != nil {
		return nil, err
	}
	i.Provider.Release = &release
	i.initOnce.Do(func() {
		i.initErr = i.Updater.Init(wailsupdater.Config{CurrentVersion: i.Provider.Current.Version, Providers: []wailsupdater.Provider{i.Provider}, Platform: i.Provider.Current.OS, Arch: i.Provider.Current.Arch, Channel: release.Metadata.Channel, Window: wailsupdater.WindowNone})
	})
	if i.initErr != nil {
		return nil, i.initErr
	}
	if _, err := i.Updater.Check(ctx); err != nil {
		return nil, err
	}
	if err := i.Updater.DownloadAndInstall(ctx); err != nil {
		return nil, err
	}
	downloaded := i.Updater.DownloadedPath()
	staged.ArtifactPath = staged.InstallPath + ".openvibely-new"
	if err := validateDesktopDataBoundaries(staged, i.ProtectedDataPaths); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(staged.ArtifactPath); err != nil {
		return nil, err
	}
	if err := copyFilesystemPath(downloaded, staged.ArtifactPath); err != nil {
		_ = os.RemoveAll(staged.ArtifactPath)
		return nil, err
	}
	if info, err := os.Stat(staged.ArtifactPath); err != nil {
		return nil, err
	} else if info.IsDir() {
		if err := syncFilesystemTree(staged.ArtifactPath); err != nil {
			return nil, err
		}
	}
	if err := syncDirectory(filepath.Dir(staged.ArtifactPath)); err != nil {
		return nil, err
	}
	return staged, nil
}

func (i *WailsInstaller) startDesktopHelper(ctx context.Context, staged LocalStagedUpdate, recovery bool) error {
	if i.Shutdown == nil || i.HealthURL == "" {
		return errors.New("desktop update helper handoff is unavailable")
	}
	if err := validateDesktopDataBoundaries(staged, i.ProtectedDataPaths); err != nil {
		return err
	}
	executable, err := currentExecutablePath()
	if err != nil {
		return err
	}
	relative, err := desktopExecutableRelative(staged.InstallPath, executable)
	if err != nil {
		return err
	}
	runningVersion := ""
	if recovery {
		runningVersion = i.Provider.Current.Version
	}
	return startPackagedUpdateHelperHandoff(ctx, packagedUpdateHelperHandoffConfig{
		Staged:             staged,
		HelperSourcePath:   executable,
		CommandName:        "desktop-update-helper",
		HealthURL:          i.HealthURL,
		Recovery:           recovery,
		RunningVersion:     runningVersion,
		RelaunchMetadata:   binaryRelaunchMetadata{Arguments: append([]string(nil), i.Arguments...), WorkingDirectory: i.WorkingDirectory, ExecutableRelative: filepath.ToSlash(relative)},
		MetadataTransport:  packagedHelperMetadataStdin,
		StartHelper:        i.StartHelper,
		AwaitHelperHandoff: i.awaitHelperHandoff,
		Shutdown:           i.Shutdown,
	})
}

type packagedUpdateHelperMetadataTransport int

const (
	packagedHelperMetadataStdin packagedUpdateHelperMetadataTransport = iota + 1
	packagedHelperMetadataFile
)

type packagedUpdateHelperHandoffConfig struct {
	Staged             LocalStagedUpdate
	HelperSourcePath   string
	CommandName        string
	HealthURL          string
	Recovery           bool
	RunningVersion     string
	RelaunchMetadata   binaryRelaunchMetadata
	MetadataTransport  packagedUpdateHelperMetadataTransport
	StartHelper        func(*exec.Cmd) error
	AwaitHelperHandoff func(context.Context, LocalStagedUpdate) error
	Shutdown           func()

	OnSetupFailure   func(helperPath string)
	OnStartFailure   func(helperPath, metadataPath string)
	OnHandoffFailure func(metadataPath string)
}

func startPackagedUpdateHelperHandoff(ctx context.Context, cfg packagedUpdateHelperHandoffConfig) error {
	staged := cfg.Staged
	helperPath, err := publishPackagedUpdateHelper(cfg.HelperSourcePath, staged.InstallPath)
	if err != nil {
		return err
	}
	if cfg.Recovery {
		_ = os.Remove(binaryHelperRecoveryReadyPath(staged.InstallPath))
	} else {
		if err := removeBinaryHelperOutcome(staged); err != nil {
			if cfg.OnSetupFailure != nil {
				cfg.OnSetupFailure(helperPath)
			}
			return err
		}
		if err := writeBinaryHelperOutcome(staged, binaryOutcomePrepared); err != nil {
			if cfg.OnSetupFailure != nil {
				cfg.OnSetupFailure(helperPath)
			}
			return err
		}
	}
	metadata, err := json.Marshal(cfg.RelaunchMetadata)
	if err != nil {
		return err
	}
	metadataPath := ""
	args := []string{cfg.CommandName, "--parent-pid", fmt.Sprint(os.Getpid()), "--current", staged.InstallPath, "--staged", staged.ArtifactPath, "--backup", staged.BackupPath, "--health-url", cfg.HealthURL, "--expected-version", staged.Version, "--previous-version", staged.PreviousVersion, "--outcome-id", staged.OutcomeID}
	if cfg.Recovery {
		args = append(args, "--recovery", "true", "--running-version", cfg.RunningVersion)
	}
	switch cfg.MetadataTransport {
	case packagedHelperMetadataStdin:
	case packagedHelperMetadataFile:
		metadataPath = binaryHelperRelaunchMetadataPath(staged.InstallPath)
		if err := atomicWriteState(metadataPath, metadata); err != nil {
			return err
		}
		args = append(args, "--relaunch-metadata", metadataPath)
	default:
		return errors.New("packaged update helper relaunch metadata transport is unavailable")
	}
	cmd := exec.Command(helperPath, args...)
	configureDetachedHelper(cmd)
	if cfg.MetadataTransport == packagedHelperMetadataStdin {
		cmd.Stdin = bytes.NewReader(metadata)
	}
	if err := startDetachedHelper(cmd, cfg.StartHelper); err != nil {
		if cfg.OnStartFailure != nil {
			cfg.OnStartFailure(helperPath, metadataPath)
		}
		return err
	}
	if cfg.Recovery {
		if err := waitForPackagedUpdateHelperRecoveryReadiness(ctx, staged); err != nil {
			return err
		}
	} else {
		await := cfg.AwaitHelperHandoff
		if await == nil {
			await = waitForBinaryHelperHandoff
		}
		if err := await(ctx, staged); err != nil {
			if cfg.OnHandoffFailure != nil {
				cfg.OnHandoffFailure(metadataPath)
			}
			return err
		}
	}
	cfg.Shutdown()
	return nil
}

func publishPackagedUpdateHelper(sourcePath, installPath string) (string, error) {
	helperPath := packagedUpdateHelperPath(installPath)
	info, err := os.Stat(sourcePath)
	if err != nil {
		return "", err
	}
	if err := publishBinaryBackup(sourcePath, helperPath, info.Mode().Perm()); err != nil {
		return "", err
	}
	return helperPath, nil
}

func currentExecutablePath() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(executable)
}

func desktopExecutableRelative(installPath, executable string) (string, error) {
	installInfo, err := os.Stat(installPath)
	if err != nil || !installInfo.IsDir() {
		return "", nil
	}
	relative, err := filepath.Rel(installPath, executable)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("desktop executable is outside the application install unit")
	}
	return relative, nil
}

func waitForPackagedUpdateHelperRecoveryReadiness(ctx context.Context, staged LocalStagedUpdate) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		outcome, err := readBinaryHelperOutcomeAt(staged, binaryHelperRecoveryReadyPath(staged.InstallPath))
		if err == nil && outcome.State == binaryOutcomeRecovering {
			return nil
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func startDetachedHelper(cmd *exec.Cmd, start func(*exec.Cmd) error) error {
	if start == nil {
		start = func(command *exec.Cmd) error { return command.Start() }
	}
	if err := start(cmd); err != nil {
		retry := cloneHelperCommand(cmd)
		if !relaxDetachedHelperBreakaway(retry) {
			return err
		}
		if retryErr := start(retry); retryErr != nil {
			return errors.Join(err, retryErr)
		}
	}
	return nil
}

func cloneHelperCommand(cmd *exec.Cmd) *exec.Cmd {
	if cmd == nil {
		return nil
	}
	retry := &exec.Cmd{
		Path:       cmd.Path,
		Args:       append([]string(nil), cmd.Args...),
		Env:        append([]string(nil), cmd.Env...),
		Dir:        cmd.Dir,
		Stdin:      cmd.Stdin,
		Stdout:     cmd.Stdout,
		Stderr:     cmd.Stderr,
		ExtraFiles: append([]*os.File(nil), cmd.ExtraFiles...),
	}
	if cmd.SysProcAttr != nil {
		attributes := *cmd.SysProcAttr
		retry.SysProcAttr = &attributes
	}
	return retry
}

func (i *WailsInstaller) Apply(ctx context.Context, value any) error {
	staged, ok := value.(LocalStagedUpdate)
	if !ok {
		return errors.New("invalid Wails desktop staged update")
	}
	if err := retainDesktopInstallUnit(staged, i.ProtectedDataPaths); err != nil {
		return err
	}
	return i.startDesktopHelper(ctx, staged, false)
}
func (i *WailsInstaller) Validate(_ context.Context, release ReleaseMetadata) error {
	if release.Version == "" {
		return errors.New("desktop release version is empty")
	}
	return nil
}
func (i *WailsInstaller) Rollback(_ context.Context, value any) error {
	staged, ok := value.(LocalStagedUpdate)
	if !ok {
		return errors.New("invalid Wails desktop staged update")
	}
	if err := restoreDesktopInstallUnit(staged, i.ProtectedDataPaths); err != nil {
		return err
	}
	relaunch := i.Relaunch
	if relaunch == nil {
		relaunch = func(path string) error {
			if runtime.GOOS == "darwin" && filepath.Ext(path) == ".app" {
				return exec.Command("open", "-n", path).Start()
			}
			return exec.Command(path).Start()
		}
	}
	return relaunch(staged.InstallPath)
}
func (i *WailsInstaller) RequiresRestartValidation() bool { return true }
func (i *WailsInstaller) RecoveryReady() bool {
	return strings.TrimSpace(i.HealthURL) != "" && i.Shutdown != nil
}
func (i *WailsInstaller) RecoverBinaryRestart(ctx context.Context, staged LocalStagedUpdate) error {
	return i.startDesktopHelper(ctx, staged, true)
}

func currentApplicationBundle() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	path, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	if runtime.GOOS != "darwin" {
		return path, nil
	}
	for path != filepath.Dir(path) {
		if filepath.Ext(path) == ".app" {
			return path, nil
		}
		path = filepath.Dir(path)
	}
	return "", errors.New("desktop executable is not inside an application bundle")
}

func validateDesktopDataBoundaries(staged LocalStagedUpdate, protectedPaths []string) error {
	if !filepath.IsAbs(staged.InstallPath) || staged.BackupPath != staged.InstallPath+".openvibely-backup" {
		return errors.New("desktop backup paths must identify an absolute application install unit")
	}
	info, err := os.Stat(staged.InstallPath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if filepath.Ext(staged.InstallPath) != ".app" {
			return errors.New("desktop application directory must be a macOS app bundle")
		}
	} else if !info.Mode().IsRegular() {
		return errors.New("desktop application executable must be a regular file")
	}
	if len(protectedPaths) == 0 {
		return errors.New("desktop protected data paths are required")
	}
	helperPath := packagedUpdateHelperPath(staged.InstallPath)
	boundaries := []string{
		staged.InstallPath,
		staged.BackupPath,
		staged.BackupPath + ".partial",
		staged.BackupPath + ".stale",
		staged.InstallPath + ".openvibely-failed",
		staged.InstallPath + ".bak",
		helperPath,
		helperPath + ".partial",
	}
	if staged.OutcomeID != "" {
		for _, statePath := range []string{
			binaryHelperOutcomePath(staged.InstallPath),
			binaryHelperPreparedPath(staged.InstallPath),
			binaryHelperAuthorizedPath(staged.InstallPath),
			binaryHelperCancelledPath(staged.InstallPath),
			binaryHelperRecoveryReadyPath(staged.InstallPath),
			binaryHelperRecoveryClaimPath(staged.InstallPath),
			binaryHelperTransitionLeasePath(staged),
			binaryHelperLeasePath(staged),
		} {
			boundaries = append(boundaries, statePath, statePath+".tmp")
		}
	}
	if staged.ArtifactPath != "" {
		boundaries = append(boundaries, staged.ArtifactPath)
	}
	for _, protectedPath := range protectedPaths {
		if !filepath.IsAbs(protectedPath) {
			return errors.New("desktop protected data paths must be absolute")
		}
		resolvedDataPath, err := canonicalPathAllowMissing(protectedPath)
		if err != nil {
			return err
		}
		for _, boundary := range boundaries {
			resolvedBoundary, err := canonicalPathAllowMissing(boundary)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(resolvedBoundary, resolvedDataPath)
			if err != nil {
				return err
			}
			if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
				return errors.New("desktop user data must live outside the replaceable application install unit")
			}
		}
	}
	return nil
}

func canonicalPathAllowMissing(path string) (string, error) {
	path = filepath.Clean(path)
	current := path
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path, nil
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func retainDesktopInstallUnit(staged LocalStagedUpdate, protectedPaths []string) error {
	if err := validateDesktopDataBoundaries(staged, protectedPaths); err != nil {
		return err
	}
	partial := staged.BackupPath + ".partial"
	stale := staged.BackupPath + ".stale"
	if err := os.RemoveAll(partial); err != nil {
		return err
	}
	if err := os.RemoveAll(stale); err != nil {
		return err
	}
	if _, err := os.Stat(staged.BackupPath); err == nil {
		if err := os.Rename(staged.BackupPath, stale); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(staged.BackupPath)); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := copyFilesystemPath(staged.InstallPath, partial); err != nil {
		_ = os.RemoveAll(partial)
		return err
	}
	if info, err := os.Stat(partial); err != nil {
		_ = os.RemoveAll(partial)
		return err
	} else if info.IsDir() {
		if err := syncFilesystemTree(partial); err != nil {
			_ = os.RemoveAll(partial)
			return err
		}
	}
	if err := os.Rename(partial, staged.BackupPath); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(staged.BackupPath)); err != nil {
		return err
	}
	if err := os.RemoveAll(stale); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(staged.BackupPath))
}

func restoreDesktopInstallUnit(staged LocalStagedUpdate, protectedPaths []string) error {
	if err := validateDesktopDataBoundaries(staged, protectedPaths); err != nil {
		return err
	}
	if _, err := os.Stat(staged.BackupPath); err != nil {
		return err
	}
	failed := staged.InstallPath + ".openvibely-failed"
	if err := os.RemoveAll(failed); err != nil {
		return err
	}
	if err := os.Rename(staged.InstallPath, failed); err != nil {
		return err
	}
	if err := os.Rename(staged.BackupPath, staged.InstallPath); err != nil {
		_ = os.Rename(failed, staged.InstallPath)
		return err
	}
	return os.RemoveAll(failed)
}

func retainDesktopBundle(staged LocalStagedUpdate, applicationDataPath string) error {
	return retainDesktopInstallUnit(staged, []string{applicationDataPath})
}

func restoreDesktopBundle(staged LocalStagedUpdate, applicationDataPath string) error {
	return restoreDesktopInstallUnit(staged, []string{applicationDataPath})
}

func copyFilesystemPath(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return copyFilesystemTree(source, destination)
	}
	if !info.Mode().IsRegular() {
		return errors.New("desktop install unit is not a regular file or application bundle")
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func copyFilesystemTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported file in desktop bundle: %s", path)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		inputCloseErr := input.Close()
		syncErr := output.Sync()
		outputCloseErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if inputCloseErr != nil {
			return inputCloseErr
		}
		if syncErr != nil {
			return syncErr
		}
		return outputCloseErr
	})
}

func syncFilesystemTree(root string) error {
	var directories []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			directories = append(directories, path)
		}
		return nil
	}); err != nil {
		return err
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := syncDirectory(directories[index]); err != nil {
			return err
		}
	}
	return nil
}

func syncDirectory(path string) error {
	// Windows does not support flushing a directory handle: os.File.Sync
	// returns ERROR_ACCESS_DENIED. File contents are synced before publication
	// and MoveFileEx uses WRITE_THROUGH for replacements on Windows.
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

type DesktopInstaller struct {
	Client     *Client
	Current    CurrentBuild
	AppPath    string
	StagingDir string
	Relaunch   func(string) error
}

func (i *DesktopInstaller) Stage(ctx context.Context, release VerifiedRelease) (any, error) {
	if runtime.GOOS != "darwin" || release.Target.Kind != "app_bundle" || release.Target.Filetype != "zip" {
		return nil, errors.New("desktop installer requires a macOS app_bundle zip")
	}
	if err := i.Client.ValidateForInstall(release, i.Current); err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp(i.StagingDir, ".openvibely-desktop-update-")
	if err != nil {
		return nil, err
	}
	archive := filepath.Join(root, "OpenVibely.app.zip")
	artifact, err := i.Client.Fetch(ctx, release, archive)
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	bundle, err := extractApplicationBundle(artifact.Path, filepath.Join(root, "extracted"))
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	return LocalStagedUpdate{ArtifactPath: bundle, InstallPath: i.AppPath, BackupPath: i.AppPath + ".openvibely-backup", Version: release.Metadata.Version}, nil
}
func (i *DesktopInstaller) Apply(_ context.Context, value any) error {
	staged, ok := value.(LocalStagedUpdate)
	if !ok {
		return errors.New("invalid desktop staged update")
	}
	if !filepath.IsAbs(staged.InstallPath) || filepath.Ext(staged.InstallPath) != ".app" || filepath.Ext(staged.ArtifactPath) != ".app" {
		return errors.New("desktop update paths must be absolute app bundles")
	}
	_ = os.RemoveAll(staged.BackupPath)
	if err := os.Rename(staged.InstallPath, staged.BackupPath); err != nil {
		return err
	}
	if err := os.Rename(staged.ArtifactPath, staged.InstallPath); err != nil {
		_ = os.Rename(staged.BackupPath, staged.InstallPath)
		return err
	}
	if i.Relaunch != nil {
		if err := i.Relaunch(staged.InstallPath); err != nil {
			return err
		}
	}
	return nil
}
func (i *DesktopInstaller) Validate(_ context.Context, release ReleaseMetadata) error {
	if release.Version == "" {
		return errors.New("desktop release version is empty")
	}
	return nil
}
func (i *DesktopInstaller) Rollback(_ context.Context, value any) error {
	staged, ok := value.(LocalStagedUpdate)
	if !ok {
		return errors.New("invalid desktop staged update")
	}
	failed := staged.InstallPath + ".failed"
	_ = os.RemoveAll(failed)
	if err := os.Rename(staged.InstallPath, failed); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(staged.BackupPath, staged.InstallPath); err != nil {
		return err
	}
	_ = os.RemoveAll(failed)
	if i.Relaunch != nil {
		return i.Relaunch(staged.InstallPath)
	}
	return nil
}

func extractApplicationBundle(archivePath, destination string) (string, error) {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", err
	}
	defer r.Close()
	var root string
	for _, f := range r.File {
		clean := filepath.Clean(f.Name)
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return "", errors.New("unsafe desktop archive path")
		}
		parts := strings.Split(filepath.ToSlash(clean), "/")
		if len(parts) == 0 || !strings.HasSuffix(parts[0], ".app") {
			return "", errors.New("desktop archive must contain one app bundle at its root")
		}
		if root == "" {
			root = parts[0]
		} else if root != parts[0] {
			return "", errors.New("desktop archive contains multiple app bundles")
		}
		target := filepath.Join(destination, filepath.FromSlash(clean))
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, f.Mode().Perm()); err != nil {
				return "", err
			}
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("desktop archive symlinks are not supported")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, f.Mode().Perm())
		if err != nil {
			rc.Close()
			return "", err
		}
		_, copyErr := io.Copy(out, rc)
		closeErr := out.Close()
		rc.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	if root == "" {
		return "", errors.New("desktop archive is empty")
	}
	return filepath.Join(destination, root), nil
}

type BinaryInstaller struct {
	Client             *Client
	Current            CurrentBuild
	Executable         string
	HealthURL          string
	StartHelper        func(*exec.Cmd) error
	awaitHelperHandoff func(context.Context, LocalStagedUpdate) error
	Shutdown           func()
	Arguments          []string
	WorkingDirectory   string
}

func (i *BinaryInstaller) Stage(ctx context.Context, release VerifiedRelease) (any, error) {
	if release.Target.Kind != "executable" && release.Target.Kind != "binary" {
		return nil, errors.New("binary installer requires an executable target")
	}
	if err := i.Client.ValidateForInstall(release, i.Current); err != nil {
		return nil, err
	}
	format, err := packagedBinaryFormat(release.Target, i.Current.OS)
	if err != nil {
		return nil, err
	}
	exe, err := filepath.Abs(i.Executable)
	if err != nil {
		return nil, err
	}
	path := exe + ".openvibely-new"
	archivePath := exe + ".openvibely-package"
	_ = os.Remove(path)
	_ = os.Remove(archivePath)
	defer os.Remove(archivePath)
	artifact, err := i.Client.Fetch(ctx, release, archivePath)
	if err != nil {
		return nil, err
	}
	archive, err := os.Open(artifact.Path)
	if err != nil {
		return nil, err
	}
	extractErr := extractPackagedBinary(archive, release.Target.Size, format, path)
	closeErr := archive.Close()
	if extractErr != nil {
		return nil, extractErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return nil, closeErr
	}
	info, err := os.Stat(exe)
	if err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	if err := os.Chmod(path, info.Mode().Perm()); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return LocalStagedUpdate{ArtifactPath: path, InstallPath: exe, BackupPath: exe + ".openvibely-backup", Version: release.Metadata.Version, PreviousVersion: i.Current.Version, OutcomeID: randomGeneration()}, nil
}

const maxPackagedBinarySize = 512 << 20

func packagedBinaryFormat(target Target, goos string) (string, error) {
	if target.OS != "" && goos != "" && target.OS != goos {
		return "", errors.New("binary package platform does not match the running application")
	}
	format := strings.ToLower(strings.TrimSpace(target.Filetype))
	if format == "" {
		switch {
		case strings.HasSuffix(strings.ToLower(target.Filename), ".tar.gz"):
			format = "tar.gz"
		case strings.HasSuffix(strings.ToLower(target.Filename), ".zip"):
			format = "zip"
		}
	}
	switch goos {
	case "darwin", "windows":
		if format != "zip" {
			return "", fmt.Errorf("%s standalone updates require a zip package", goos)
		}
	case "linux":
		if format != "tar.gz" {
			return "", errors.New("linux standalone updates require a tar.gz package")
		}
	default:
		return "", fmt.Errorf("unsupported standalone update platform %q", goos)
	}
	return format, nil
}

func extractPackagedBinary(reader io.Reader, archiveSize int64, format, destination string) (err error) {
	if archiveSize <= 0 || archiveSize > maxPackagedBinarySize {
		return errors.New("binary package size is invalid")
	}
	archive, err := io.ReadAll(io.LimitReader(reader, archiveSize+1))
	if err != nil {
		return err
	}
	if int64(len(archive)) != archiveSize {
		return errors.New("binary package size changed after verification")
	}
	partial := destination + ".partial"
	if err := os.Remove(partial); err != nil && !os.IsNotExist(err) {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(partial)
			_ = os.Remove(destination)
		}
	}()
	var payload io.Reader
	var mode os.FileMode
	var expectedSize int64
	switch format {
	case "zip":
		zr, openErr := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if openErr != nil {
			return openErr
		}
		if len(zr.File) != 1 {
			return errors.New("binary zip package must contain exactly one executable")
		}
		entry := zr.File[0]
		if !validPackagedBinaryName(entry.Name) || !entry.Mode().IsRegular() || entry.UncompressedSize64 > maxPackagedBinarySize {
			return errors.New("binary zip package contains an invalid executable entry")
		}
		rc, openErr := entry.Open()
		if openErr != nil {
			return openErr
		}
		defer rc.Close()
		payload, mode, expectedSize = rc, entry.Mode(), int64(entry.UncompressedSize64)
	case "tar.gz":
		gz, openErr := gzip.NewReader(bytes.NewReader(archive))
		if openErr != nil {
			return openErr
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		header, nextErr := tr.Next()
		if nextErr != nil {
			return nextErr
		}
		if !validPackagedBinaryName(header.Name) || header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > maxPackagedBinarySize {
			return errors.New("binary tar package contains an invalid executable entry")
		}
		payload, mode, expectedSize = io.LimitReader(tr, header.Size), os.FileMode(header.Mode), header.Size
		defer func() {
			if err == nil {
				if _, nextErr := tr.Next(); nextErr != io.EOF {
					err = errors.New("binary tar package must contain exactly one executable")
				}
			}
		}()
	default:
		return errors.New("unsupported binary package format")
	}
	output, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm()|0o500)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(output, io.LimitReader(payload, maxPackagedBinarySize+1))
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if written <= 0 || written > maxPackagedBinarySize || written != expectedSize {
		return errors.New("binary package executable size is invalid")
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(partial, destination); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(destination))
}

func validPackagedBinaryName(name string) bool {
	return name == "openvibely" || name == "openvibely.exe"
}

func (i *BinaryInstaller) Apply(ctx context.Context, value any) error {
	staged, ok := value.(LocalStagedUpdate)
	if !ok {
		return errors.New("invalid binary staged update")
	}
	if i.Shutdown == nil {
		return errors.New("binary update shutdown handoff is unavailable")
	}
	if err := validateBinaryPaths(staged); err != nil {
		return err
	}
	err := startPackagedUpdateHelperHandoff(ctx, packagedUpdateHelperHandoffConfig{
		Staged:             staged,
		HelperSourcePath:   staged.InstallPath,
		CommandName:        "update-helper",
		HealthURL:          i.HealthURL,
		RelaunchMetadata:   binaryRelaunchMetadata{Arguments: append([]string(nil), i.Arguments...), WorkingDirectory: i.WorkingDirectory},
		MetadataTransport:  packagedHelperMetadataFile,
		StartHelper:        i.StartHelper,
		AwaitHelperHandoff: i.awaitHelperHandoff,
		Shutdown:           i.Shutdown,
		OnSetupFailure: func(helperPath string) {
			_ = os.Remove(helperPath)
		},
		OnStartFailure: func(helperPath, metadataPath string) {
			_ = os.Remove(helperPath)
			_ = os.Remove(metadataPath)
			_ = removeBinaryHelperOutcome(staged)
		},
		OnHandoffFailure: func(metadataPath string) {
			_ = os.Remove(metadataPath)
		},
	})
	if err != nil {
		return err
	}
	return nil
}

func (i *BinaryInstaller) RecoverBinaryRestart(ctx context.Context, staged LocalStagedUpdate) error {
	if i.Shutdown == nil {
		return errors.New("binary recovery shutdown handoff is unavailable")
	}
	if err := validateBinaryPaths(staged); err != nil {
		return err
	}
	err := startPackagedUpdateHelperHandoff(ctx, packagedUpdateHelperHandoffConfig{
		Staged:            staged,
		HelperSourcePath:  staged.InstallPath,
		CommandName:       "update-helper",
		HealthURL:         i.HealthURL,
		Recovery:          true,
		RunningVersion:    i.Current.Version,
		RelaunchMetadata:  binaryRelaunchMetadata{Arguments: append([]string(nil), i.Arguments...), WorkingDirectory: i.WorkingDirectory},
		MetadataTransport: packagedHelperMetadataFile,
		StartHelper:       i.StartHelper,
		Shutdown:          i.Shutdown,
		OnStartFailure: func(_, metadataPath string) {
			_ = os.Remove(metadataPath)
		},
	})
	if err != nil {
		return fmt.Errorf("binary recovery helper handoff: %w", err)
	}
	return nil
}

func waitForBinaryHelperHandoff(ctx context.Context, staged LocalStagedUpdate) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		outcome, err := readBinaryHelperOutcome(staged)
		if err == nil && outcome.State == binaryOutcomePending {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := authorizeBinaryHelperHandoff(staged); err != nil {
				return err
			}
			return nil
		}
		if err == nil && outcome.State == binaryOutcomeAuthorized {
			return nil
		}
		if err == nil && outcome.State == binaryOutcomeCancelled {
			return errors.New("binary helper handoff was cancelled")
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func (i *BinaryInstaller) Validate(_ context.Context, release ReleaseMetadata) error {
	if release.Version == "" {
		return errors.New("binary release version is empty")
	}
	return nil
}
func (i *BinaryInstaller) Rollback(_ context.Context, value any) error {
	staged, ok := value.(LocalStagedUpdate)
	if !ok {
		return errors.New("invalid binary staged update")
	}
	if _, err := os.Stat(staged.BackupPath); err != nil {
		return err
	}
	_ = os.Remove(staged.InstallPath)
	return os.Rename(staged.BackupPath, staged.InstallPath)
}

func (i *BinaryInstaller) RequiresRestartValidation() bool { return true }
func (i *BinaryInstaller) RecoveryReady() bool             { return strings.TrimSpace(i.HealthURL) != "" }

func validateBinaryPaths(s LocalStagedUpdate) error {
	if !filepath.IsAbs(s.InstallPath) || !filepath.IsAbs(s.ArtifactPath) || !filepath.IsAbs(s.BackupPath) {
		return errors.New("binary update paths must be absolute")
	}
	dir := filepath.Dir(s.InstallPath)
	if filepath.Dir(s.ArtifactPath) != dir || filepath.Dir(s.BackupPath) != dir || s.ArtifactPath != s.InstallPath+".openvibely-new" || s.BackupPath != s.InstallPath+".openvibely-backup" {
		return errors.New("binary update paths must use validated sibling names")
	}
	return nil
}
