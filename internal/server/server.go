// Package server provides a reusable bootstrap function that wires and starts
// the full OpenVibely backend (database, repos, services, HTTP routes, background
// workers, and graceful shutdown).  It is consumed by both cmd/server (web/VPS)
// and cmd/desktop (Wails desktop wrapper).
package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/agentplugins"
	"github.com/openvibely/openvibely/internal/agentskills"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/buildinfo"
	"github.com/openvibely/openvibely/internal/builtinskills"
	"github.com/openvibely/openvibely/internal/config"
	"github.com/openvibely/openvibely/internal/database"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/handler"
	"github.com/openvibely/openvibely/internal/lifecycle"
	"github.com/openvibely/openvibely/internal/memory"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/update"

	_ "github.com/openvibely/openvibely/docs" // Swagger docs
)

// Instance holds a running server's state so callers can inspect the bound
// address and trigger graceful shutdown.
type Instance struct {
	// BoundAddr is the address the HTTP server is listening on (e.g. "127.0.0.1:54321").
	BoundAddr string
	// BaseURL is the full http:// URL including scheme and bound address.
	BaseURL string
	// Shutdown gracefully stops all background services, the HTTP server, and the DB.
	// It is safe to call multiple times.
	Shutdown          func()
	ShutdownRequested <-chan struct{}
	UpdateCoordinator *update.Coordinator
}

func migrateLegacyStorage(cfg *config.Config) error {
	if cfg == nil || os.Getenv("OPENVIBELY_DISABLE_LEGACY_STORAGE_MIGRATION") != "" {
		return nil
	}
	if os.Getenv("OPENVIBELY_APP_DATA_DIR") != "" {
		return nil
	}
	if os.Getenv("DATABASE_PATH") == "" {
		if err := migrateLegacyDatabaseFiles(cfg.DatabasePath); err != nil {
			return err
		}
	}
	if os.Getenv("PROJECT_REPO_ROOT") == "" {
		if err := migrateLegacyRepoRoot(cfg.ProjectRepoRoot); err != nil {
			return err
		}
	}
	if err := migrateLegacyDirectory("uploads", filepath.Join(cfg.AppDataDir, "uploads")); err != nil {
		return err
	}
	return nil
}

func migrateLegacyDatabaseFiles(databasePath string) error {
	if strings.TrimSpace(databasePath) == "" {
		return nil
	}
	targetAbs, err := filepath.Abs(databasePath)
	if err != nil {
		return fmt.Errorf("resolving database path: %w", err)
	}
	legacyAbs, err := firstExistingLegacyPath("openvibely.db", targetAbs)
	if err != nil {
		return err
	}
	if legacyAbs == "" {
		return nil
	}
	if info, err := os.Stat(targetAbs); err == nil {
		if info.IsDir() {
			return fmt.Errorf("target database path %s exists and is a directory", targetAbs)
		}
		if info.Size() > 0 && isSQLiteDatabaseFile(targetAbs) {
			applog.Infof("[storage] skipped legacy database migration from %s because target database already exists at %s", legacyAbs, targetAbs)
			return nil
		}
		applog.Infof("[storage] migrating legacy database from %s over empty or invalid target database at %s", legacyAbs, targetAbs)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking target database path %s: %w", targetAbs, err)
	}
	if err := os.MkdirAll(filepath.Dir(targetAbs), 0o755); err != nil {
		return fmt.Errorf("creating database directory %s: %w", filepath.Dir(targetAbs), err)
	}
	for _, suffix := range databaseFileSuffixes() {
		if err := backupExistingPath(targetAbs + suffix); err != nil {
			return err
		}
	}
	for _, suffix := range databaseFileSuffixes() {
		from := legacyAbs + suffix
		to := targetAbs + suffix
		if _, err := os.Stat(from); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return fmt.Errorf("checking legacy database file %s: %w", from, err)
		}
		if err := moveOrCopyPath(from, to); err != nil {
			return fmt.Errorf("moving legacy database file %s to %s: %w", from, to, err)
		}
		applog.Infof("[storage] moved legacy database file %s to %s", from, to)
	}
	return nil
}

func migrateLegacyRepoRoot(projectRepoRoot string) error {
	return migrateLegacyDirectory("repos", projectRepoRoot)
}

func migrateLegacyDirectory(name, targetPath string) error {
	if strings.TrimSpace(targetPath) == "" {
		return nil
	}
	targetAbs, err := filepath.Abs(targetPath)
	if err != nil {
		return fmt.Errorf("resolving %s target: %w", name, err)
	}
	legacyAbs, err := firstExistingLegacyPath(name, targetAbs)
	if err != nil {
		return err
	}
	if legacyAbs == "" {
		return nil
	}
	info, err := os.Stat(legacyAbs)
	if err != nil {
		return fmt.Errorf("checking legacy %s %s: %w", name, legacyAbs, err)
	}
	if !info.IsDir() {
		return nil
	}
	if targetInfo, err := os.Stat(targetAbs); err == nil {
		if !targetInfo.IsDir() {
			return fmt.Errorf("target %s path %s exists and is not a directory", name, targetAbs)
		}
		empty, emptyErr := isDirEmpty(targetAbs)
		if emptyErr != nil {
			return fmt.Errorf("checking target %s directory %s: %w", name, targetAbs, emptyErr)
		}
		if !empty {
			applog.Infof("[storage] skipped legacy %s migration from %s because target already exists at %s", name, legacyAbs, targetAbs)
			return nil
		}
		applog.Infof("[storage] migrating legacy %s from %s over empty target at %s", name, legacyAbs, targetAbs)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking target %s path %s: %w", name, targetAbs, err)
	}
	if err := os.MkdirAll(filepath.Dir(targetAbs), 0o755); err != nil {
		return fmt.Errorf("creating %s parent %s: %w", name, filepath.Dir(targetAbs), err)
	}
	if err := backupExistingPath(targetAbs); err != nil {
		return err
	}
	if err := moveOrCopyPath(legacyAbs, targetAbs); err != nil {
		return fmt.Errorf("moving legacy %s %s to %s: %w", name, legacyAbs, targetAbs, err)
	}
	applog.Infof("[storage] moved legacy %s %s to %s", name, legacyAbs, targetAbs)
	return nil
}

func databaseFileSuffixes() []string {
	return []string{"", "-wal", "-shm", "-journal"}
}

func firstExistingLegacyPath(name, targetAbs string) (string, error) {
	seen := map[string]bool{}
	for _, base := range legacyStorageSearchDirs() {
		candidate, err := filepath.Abs(filepath.Join(base, name))
		if err != nil {
			return "", fmt.Errorf("resolving legacy path %s: %w", filepath.Join(base, name), err)
		}
		if candidate == targetAbs || seen[candidate] {
			continue
		}
		seen[candidate] = true
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("checking legacy path %s: %w", candidate, err)
		}
	}
	return "", nil
}

func legacyStorageSearchDirs() []string {
	dirs := []string{"."}
	if exe, err := os.Executable(); err == nil && exe != "" {
		dirs = append(dirs, filepath.Dir(exe), filepath.Dir(filepath.Dir(exe)))
	}
	return dirs
}

func backupExistingPath(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("checking target path %s: %w", path, err)
	}
	backup := path + ".pre-appdata-migration-backup"
	for i := 1; ; i++ {
		candidate := backup
		if i > 1 {
			candidate = fmt.Sprintf("%s.%d", backup, i)
		}
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			if err := os.Rename(path, candidate); err != nil {
				return fmt.Errorf("backing up existing target path %s to %s: %w", path, candidate, err)
			}
			applog.Infof("[storage] backed up existing target path %s to %s", path, candidate)
			return nil
		} else if err != nil {
			return fmt.Errorf("checking backup path %s: %w", candidate, err)
		}
	}
}

func isDirEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

func isSQLiteDatabaseFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	header := make([]byte, 16)
	if _, err := io.ReadFull(f, header); err != nil {
		return false
	}
	return string(header) == "SQLite format 3\x00"
}

func moveOrCopyPath(from, to string) error {
	if err := os.Rename(from, to); err == nil {
		return nil
	} else if !isCrossDeviceRename(err) {
		return err
	}
	info, err := os.Stat(from)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := copyDir(from, to); err != nil {
			return err
		}
		return os.RemoveAll(from)
	}
	if err := copyFile(from, to, info.Mode()); err != nil {
		return err
	}
	return os.Remove(from)
}

func isCrossDeviceRename(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "cross-device") || strings.Contains(strings.ToLower(err.Error()), "invalid cross-device link")
}

func copyFile(from, to string, mode os.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(to)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(to)
		return closeErr
	}
	return nil
}

func copyDir(from, to string) error {
	return filepath.WalkDir(from, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(to, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(dest, info.Mode())
		}
		return copyFile(path, dest, info.Mode())
	})
}

func requestLoggerConfig(output io.Writer) middleware.LoggerConfig {
	cfg := middleware.LoggerConfig{
		Format: "${time_rfc3339} method=${method} path=${path} status=${status} latency=${latency_human} request_id=${id}\n",
	}
	if output != nil {
		cfg.Output = output
	}
	return cfg
}

func configureMethodOverride(e *echo.Echo) {
	authProtocolPaths := map[string]struct{}{
		"/login": {}, "/logout": {}, "/auth/me": {}, "/auth/sso/start": {},
		"/auth/sso/callback": {}, "/logged-out": {},
	}
	e.Pre(middleware.MethodOverrideWithConfig(middleware.MethodOverrideConfig{
		Skipper: func(c echo.Context) bool {
			_, skip := authProtocolPaths[c.Request().URL.Path]
			return skip
		},
	}))
}

func desktopUpdateProtectedPaths(cfg *config.Config) ([]string, error) {
	paths := []string{cfg.AppDataDir, cfg.DatabasePath, cfg.ProjectRepoRoot}
	if cfg.Mode == config.ModeDesktop {
		paths = append(paths, config.DesktopConfigFilePath())
		if pluginRoot := strings.TrimSpace(os.Getenv("OPENVIBELY_PLUGIN_ROOT")); pluginRoot != "" {
			paths = append(paths, pluginRoot)
		}
	}
	if keyFile := strings.TrimSpace(cfg.UpdatePublicKeyFile); keyFile != "" {
		paths = append(paths, keyFile)
	}
	resolved := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		absolute = filepath.Clean(absolute)
		if _, exists := seen[absolute]; exists {
			continue
		}
		seen[absolute] = struct{}{}
		resolved = append(resolved, absolute)
	}
	return resolved, nil
}

type updateCoordinatorStarter interface {
	StartRecovery(context.Context)
	StartChecks(context.Context)
}

func startUpdateCoordinator(ctx context.Context, coordinator updateCoordinatorStarter) {
	coordinator.StartRecovery(ctx)
	coordinator.StartChecks(ctx)
}

// Start wires the full OpenVibely backend and starts serving HTTP on cfg.Port.
// It blocks until the HTTP listener is bound and background services are started,
// then returns an Instance with the bound address and a shutdown handle.
//
// The caller is responsible for calling Instance.Shutdown when done, or
// listening for OS signals and calling it from a signal handler.
func Start(ctx context.Context, cfg *config.Config) (*Instance, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	cfg.NormalizeForMode()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	if cfg.AuthMode != auth.AuthModeHostedSSO {
		if err := config.ValidateAppBaseURL(os.Getenv("APP_BASE_URL")); err != nil {
			applog.Infof("warning: %v", err)
		}
	}

	authCfg := auth.Config{
		Enabled:       cfg.AuthMode == auth.AuthModeLocal,
		Username:      cfg.AuthUsername,
		Password:      cfg.AuthPassword,
		SessionSecret: cfg.AuthSessionSecret,
		SessionTTL:    cfg.AuthSessionTTL,
	}
	if err := authCfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid auth configuration: %w", err)
	}
	var hostedSSOClient *auth.HostedSSOClient
	if cfg.AuthMode == auth.AuthModeHostedSSO {
		hostedSSOClient = auth.NewHostedSSOClient(
			cfg.HostedSSOControlURL,
			cfg.HostedSSOInstanceID,
			cfg.AppBaseURL+"/auth/sso/callback",
		)
	}

	if err := migrateLegacyStorage(cfg); err != nil {
		return nil, err
	}

	uploadsPath := filepath.Join(cfg.AppDataDir, "uploads")
	handler.SetUploadsDir(uploadsPath)

	// Database
	db, err := database.New(cfg.DatabasePath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}
	applog.Infof("database initialized")
	var databaseSchema int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version WHERE is_applied = 1`).Scan(&databaseSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("reading database schema version: %w", err)
	}

	// Event broadcasters for real-time UI updates
	broadcaster := events.NewBroadcaster()
	chatBroadcaster := events.NewChatBroadcaster()
	fileChangeBroadcaster := events.NewFileChangeBroadcaster()
	executionStreamHub := events.NewExecutionStreamHub()

	// Repositories
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	taskGoalRepo := repository.NewTaskGoalRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	if modelsList, listErr := llmConfigRepo.List(context.Background()); listErr != nil {
		applog.Infof("warning: unable to check OAuth model configuration for APP_BASE_URL validation: %v", listErr)
	} else {
		hasOAuth := false
		hasOAuthAnthropic := false
		hasOAuthOpenAI := false
		for _, modelCfg := range modelsList {
			if !modelCfg.IsOAuth() {
				continue
			}
			hasOAuth = true
			if modelCfg.Provider == models.ProviderAnthropic {
				hasOAuthAnthropic = true
			}
			if modelCfg.Provider == models.ProviderOpenAI {
				hasOAuthOpenAI = true
			}
		}

		if cfg.AppBaseURL == "" {
			if hasOAuth {
				applog.Infof("warning: APP_BASE_URL is not set while OAuth models are configured; hosted OAuth callbacks will use localhost. Set APP_BASE_URL to your public host (example: https://dubee.org).")
			}
		} else {
			applog.Infof("app base url configured for OAuth callbacks: %s", cfg.AppBaseURL)
			if hasOAuthAnthropic && strings.TrimSpace(os.Getenv("ANTHROPIC_OAUTH_CLIENT_ID")) == "" {
				applog.Infof("warning: ANTHROPIC_OAUTH_CLIENT_ID not set; hosted Anthropic OAuth will use built-in client and may be rejected by provider redirect policy.")
			}
			if hasOAuthOpenAI && strings.TrimSpace(os.Getenv("OPENAI_OAUTH_CLIENT_ID")) == "" {
				applog.Infof("warning: OPENAI_OAUTH_CLIENT_ID not set; hosted OpenAI OAuth will use built-in client and may be rejected by provider redirect policy.")
			}
		}
	}
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	workerRepo := repository.NewWorkerRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	agentRepo := repository.NewAgentRepo(db)
	alertRepo := repository.NewAlertRepo(db)
	automationRepo := repository.NewAutomationRepo(db)
	automationRepo.SetBroadcaster(broadcaster)
	execRepo.SetAutomationRepo(automationRepo)
	alertRepo.SetAutomationRepo(automationRepo)
	upcomingRepo := repository.NewUpcomingRepo(db)

	// Services
	llmSvc := service.NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetExecutionStreamHub(executionStreamHub)

	maxWorkers, _ := workerRepo.GetMaxWorkers(context.Background())
	workerSvc := service.NewWorkerService(llmSvc, maxWorkers, projectRepo)
	workerSvc.SetTaskRepo(taskRepo)
	workerSvc.SetLLMConfigRepo(llmConfigRepo)
	workerSvc.SetExecutionRepo(execRepo)
	workerSvc.SetAutomationRepo(automationRepo)
	updateTracker := update.NewWorkTracker()
	updateDrain := update.NewDrainManager(
		updateTracker.Active,
		func() int {
			var total int
			_ = db.QueryRowContext(context.Background(), `SELECT
				(SELECT COUNT(*) FROM tasks WHERE status IN ('pending','queued')) +
				(SELECT COUNT(*) FROM thread_inputs WHERE input_status = 'pending' AND input_mode = 'queued')`).Scan(&total)
			return total
		},
		2*time.Second,
		time.Now,
	)
	updateDrain.SetWorkTracker(updateTracker)
	if err := updateDrain.SetPersistence(filepath.Join(cfg.AppDataDir, "update-drain.json")); err != nil {
		db.Close()
		return nil, fmt.Errorf("restoring update drain: %w", err)
	}
	workerSvc.SetUpdateWorkTracker(updateTracker)
	llmSvc.SetUpdateWorkTracker(updateTracker)
	workerSvc.SetAdmissionGate(updateDrain.Admit)
	updateKeys, err := update.DecodePublicKeys(buildinfo.ReleaseKeyID, buildinfo.ReleasePublicKey, cfg.UpdatePublicKeyFile)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("loading update trust root: %w", err)
	}
	currentBuild := buildinfo.Current(cfg.BuildArtifact)
	currentBuild.Artifact = cfg.BuildArtifact
	updateClient := update.NewClient(update.ClientConfig{
		ServiceURL: cfg.UpdateServiceURL,
		Channel:    cfg.UpdateChannel,
		StatePath:  filepath.Join(cfg.AppDataDir, "update-state.json"),
		PublicKeys: updateKeys,
	})
	current := update.CurrentBuild{Build: currentBuild, Distribution: cfg.Distribution}
	var updateInstaller update.Installer
	var binaryUpdateInstaller *update.BinaryInstaller
	shutdownRequested := make(chan struct{}, 1)
	var hostedUpdateController *update.HostedController
	switch {
	case cfg.BuildArtifact == buildinfo.ArtifactBinary && cfg.ManagedUpdateError == "":
		executable, execErr := os.Executable()
		if execErr != nil {
			db.Close()
			return nil, fmt.Errorf("resolving update executable: %w", execErr)
		}
		workingDirectory, workdirErr := os.Getwd()
		if workdirErr != nil {
			db.Close()
			return nil, fmt.Errorf("resolving update working directory: %w", workdirErr)
		}
		binaryUpdateInstaller = &update.BinaryInstaller{Client: updateClient, Current: current, Executable: executable, Arguments: append([]string(nil), os.Args...), WorkingDirectory: workingDirectory, Shutdown: func() {
			select {
			case shutdownRequested <- struct{}{}:
			default:
			}
		}}
		updateInstaller = binaryUpdateInstaller
	case cfg.UpdateMode == buildinfo.ModeDockerAgent && cfg.ManagedUpdateError == "":
		agentAPI, apiErr := update.NewAgentHTTPClient(cfg.DockerAgentURL, cfg.DockerAgentToken, nil)
		if apiErr != nil {
			db.Close()
			return nil, fmt.Errorf("configuring Docker update agent: %w", apiErr)
		}
		dockerInstaller := &update.DockerAgentInstaller{API: agentAPI, Client: updateClient, Current: current, Drain: updateDrain, StatePath: filepath.Join(cfg.AppDataDir, "docker-update-request.json")}
		if err := dockerInstaller.Load(); err != nil {
			db.Close()
			return nil, fmt.Errorf("restoring Docker update request: %w", err)
		}
		updateInstaller = dockerInstaller
	case cfg.UpdateMode == buildinfo.ModeHosted:
		agentAPI, apiErr := update.NewAgentHTTPClient(cfg.HostedSSOControlURL, cfg.HostedAgentToken, nil)
		if apiErr != nil {
			db.Close()
			return nil, fmt.Errorf("configuring Hosted update controller: %w", apiErr)
		}
		hostedUpdateController = update.NewHostedController(agentAPI, updateDrain, current, filepath.Join(cfg.AppDataDir, "hosted-update.json"))
		if err := hostedUpdateController.Restore(); err != nil {
			db.Close()
			return nil, fmt.Errorf("restoring Hosted update controller: %w", err)
		}
	}
	updateCoordinator := update.NewCoordinator(
		updateClient,
		current,
		cfg.UpdateChannel,
		updateDrain,
		updateInstaller,
		cfg.UpdateMode == buildinfo.ModeDockerManual,
		cfg.ManagedUpdateError,
		workerSvc.ResumeDispatch,
	)
	updateCoordinator.SetUpdateNotificationsEnabled(!cfg.DisableUpdateNotifications)
	protectedDataPaths, protectedPathsErr := desktopUpdateProtectedPaths(cfg)
	if protectedPathsErr != nil {
		db.Close()
		return nil, fmt.Errorf("resolving desktop update data boundaries: %w", protectedPathsErr)
	}
	updateCoordinator.SetProtectedDataPaths(protectedDataPaths)
	if hostedUpdateController != nil {
		updateCoordinator.SetManagedStateProvider(hostedUpdateController.Lifecycle)
	}
	if err := updateCoordinator.SetPersistence(filepath.Join(cfg.AppDataDir, "update-coordinator.json")); err != nil {
		db.Close()
		return nil, fmt.Errorf("restoring update coordinator: %w", err)
	}

	projectSvc := service.NewProjectService(projectRepo)
	taskGoalSvc := service.NewTaskGoalService(taskGoalRepo, taskRepo, broadcaster)
	taskSvc := service.NewTaskService(taskRepo, attachmentRepo, workerSvc)
	taskSvc.SetDeletionUploadsDir(filepath.Join(cfg.AppDataDir, "uploads"))
	taskSvc.SetAgentRepo(agentRepo)
	taskSvc.SetTaskGoalService(taskGoalSvc)
	llmSvc.SetTaskGoalService(taskGoalSvc)
	workerSvc.SetTaskGoalService(taskGoalSvc)
	schedulerSvc := service.NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	schedulerSvc.SetUpdateWorkTracker(updateTracker)
	schedulerSvc.SetAutomationRepo(automationRepo)
	automationDispatcher := service.NewAutomationDispatcher(automationRepo, taskRepo, workerSvc)
	automationDispatcher.SetUpdateWorkTracker(updateTracker)
	automationReconciler := service.NewAutomationReconciler(automationRepo, execRepo, workerSvc)
	automationReconciler.SetUpdateWorkTracker(updateTracker)
	alertSvc := service.NewAlertService(alertRepo, broadcaster)
	automationRegistry := service.NewAutomationAdapterRegistry()
	automationRegistrationSvc := service.NewAutomationRegistrationService(automationRepo, automationRegistry)
	automationGraphSvc := service.NewAutomationGraphService(automationRepo)
	llmSvc.SetAutomationRegistrationService(automationRegistrationSvc)
	llmSvc.SetAutomationRepo(automationRepo)
	upcomingSvc := service.NewUpcomingService(upcomingRepo)
	workflowRepo := repository.NewWorkflowRepo(db)
	workflowSvc := service.NewWorkflowService(workflowRepo, llmConfigRepo, taskRepo, llmSvc)
	workflowSvc.SetUpdateWorkTracker(updateTracker)
	workflowSvc.SetAlertService(alertSvc)
	collisionRepo := repository.NewCollisionRepo(db)
	collisionSvc := service.NewCollisionService(collisionRepo, taskRepo, projectRepo, llmConfigRepo)
	collisionSvc.SetLLMService(llmSvc)
	insightsRepo := repository.NewInsightsRepo(db)
	insightsSvc := service.NewInsightsService(insightsRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)
	insightsSvc.SetLLMService(llmSvc)
	architectRepo := repository.NewArchitectRepo(db)
	architectSvc := service.NewArchitectService(architectRepo, taskRepo, projectRepo, llmConfigRepo)
	architectSvc.SetLLMService(llmSvc)
	backlogRepo := repository.NewBacklogRepo(db)
	backlogSvc := service.NewBacklogService(backlogRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)
	backlogSvc.SetLLMService(llmSvc)
	upcomingSvc.SetBacklogRepo(backlogRepo)
	upcomingSvc.SetProjectRepo(projectRepo)
	upcomingSvc.SetLLMService(llmSvc)
	upcomingSvc.SetLLMConfigRepo(llmConfigRepo)

	// Autonomous Builds (task-chain based)
	autonomousRepo := repository.NewAutonomousRepo(db)
	autonomousTriggerSvc := service.NewAutonomousTriggerService(taskSvc, projectRepo, taskRepo, execRepo, autonomousRepo)

	// Trend Intelligence (enriches autonomous builds with external data)
	trendRepo := repository.NewTrendRepo(db)
	trendSvc := service.NewTrendIntelligenceService(trendRepo, projectRepo, llmConfigRepo)
	trendSvc.SetLLMService(llmSvc)
	autonomousTriggerSvc.SetTrendIntelligenceService(trendSvc)

	// Task Templates
	templateRepo := repository.NewTemplateRepo(db)
	templateSvc := service.NewTemplateService(templateRepo, taskRepo, projectRepo)

	// Pattern Library
	patternRepo := repository.NewPatternRepo(db)
	patternSvc := service.NewPatternService(patternRepo, taskRepo)

	llmSvc.SetAlertService(alertSvc)
	llmSvc.SetTaskService(taskSvc)
	llmSvc.SetThreadInputRepo(repository.NewThreadInputRepo(db))
	llmSvc.SetBroadcaster(broadcaster)

	// Code review comments
	reviewCommentRepo := repository.NewReviewCommentRepo(db)

	// Telegram user authorization
	telegramAuthRepo := repository.NewTelegramAuthRepo(db)

	// Telegram user project persistence
	telegramUserProjectRepo := repository.NewTelegramUserProjectRepo(db)
	slackUserProjectRepo := repository.NewSlackUserProjectRepo(db)
	slackTaskContextRepo := repository.NewSlackTaskContextRepo(db)
	slackAuthRepo := repository.NewSlackAuthRepo(db)
	emailAuthRepo := repository.NewEmailAuthRepo(db)
	emailTaskContextRepo := repository.NewEmailTaskContextRepo(db)
	emailSenderProjectRepo := repository.NewEmailSenderProjectRepo(db)
	discordAuthRepo := repository.NewDiscordAuthRepo(db)
	discordTaskContextRepo := repository.NewDiscordTaskContextRepo(db)
	discordUserProjectRepo := repository.NewDiscordUserProjectRepo(db)
	channelTargetRepo := repository.NewChannelTargetRepo(db)

	// Custom personalities
	customPersonalityRepo := repository.NewCustomPersonalityRepo(db)

	settingsRepo := repository.NewSettingsRepo(db)
	automationDraftSvc := service.NewAutomationDraftService(automationRepo, automationRegistry)
	automationCapabilitySvc := service.NewAutomationCapabilitySnapshotBuilder(projectRepo, agentRepo, taskRepo, settingsRepo)
	automationDraftSvc.SetCapabilitySnapshotBuilder(automationCapabilitySvc)
	automationSaveValidator := service.NewAutomationSaveValidator(automationRegistry, automationDraftSvc)
	automationSaveValidator.SetAgentRepository(agentRepo)
	automationCompiler := service.NewAutomationCompiler(automationRepo, taskSvc, taskRepo, scheduleRepo, automationSaveValidator)
	automationCompiler.SetAgentRepository(agentRepo)
	automationLifecycleSvc := service.NewAutomationLifecycleService(automationRepo, scheduleRepo, taskSvc)
	automationConfirmationSecret, confirmationSecretErr := service.LoadOrCreateAutomationConfirmationSecret(context.Background(), settingsRepo)
	if confirmationSecretErr != nil {
		return nil, fmt.Errorf("initializing automation confirmation secret: %w", confirmationSecretErr)
	}
	automationConfirmationSvc := service.NewAutomationConfirmationService(automationRepo, execRepo, automationConfirmationSecret)
	taskPullRequestRepo := repository.NewTaskPullRequestRepo(db)
	githubPRFeedbackRepo := repository.NewGitHubPRFeedbackRepo(db)
	githubAuthRepo := repository.NewGitHubAuthRepo(db)
	automationCapabilitySvc.SetGitHubAuthRepository(githubAuthRepo)
	automationSaveValidator.SetCapabilityDependencies(projectRepo, settingsRepo, githubAuthRepo)
	webhookRepo := repository.NewWebhookRepo(db)

	// Seed Slack settings from env when provided (useful for bootstrapping local setup).
	if cfg.SlackClientID != "" {
		_ = settingsRepo.Set(context.Background(), service.SlackSettingClientID, cfg.SlackClientID)
	}
	if cfg.SlackClientSecret != "" {
		_ = settingsRepo.Set(context.Background(), service.SlackSettingClientSecret, cfg.SlackClientSecret)
	}
	if cfg.SlackAppToken != "" {
		_ = settingsRepo.Set(context.Background(), service.SlackSettingAppToken, cfg.SlackAppToken)
	}
	if cfg.SlackBotToken != "" {
		_ = settingsRepo.Set(context.Background(), service.SlackSettingBotTokenOverride, cfg.SlackBotToken)
		_ = settingsRepo.Set(context.Background(), service.SlackSettingBotTokenSource, service.SlackBotTokenSourceManual)
	}
	if val, _ := settingsRepo.Get(context.Background(), service.SlackSettingBotTokenSource); val == "" {
		_ = settingsRepo.Set(context.Background(), service.SlackSettingBotTokenSource, service.SlackBotTokenSourceOAuth)
	}
	if val, _ := settingsRepo.Get(context.Background(), service.SlackSettingSendResponses); val == "" {
		_ = settingsRepo.Set(context.Background(), service.SlackSettingSendResponses, "true")
	}
	if cfg.DiscordToken != "" {
		_ = settingsRepo.Set(context.Background(), service.DiscordSettingBotToken, cfg.DiscordToken)
	}
	if val, _ := settingsRepo.Get(context.Background(), service.DiscordSettingSendResponses); val == "" {
		_ = settingsRepo.Set(context.Background(), service.DiscordSettingSendResponses, "true")
	}

	githubSvc := service.NewGitHubService(
		settingsRepo,
		cfg.GitHubAppID,
		cfg.GitHubAppSlug,
		cfg.GitHubAppPrivateKey,
		cfg.ProjectRepoRoot,
	)
	automationExternalStateSvc := service.NewAutomationExternalStateService(automationRepo, taskPullRequestRepo, projectRepo, githubSvc)
	automationSaveValidator.SetGitHubConnectionProvider(githubSvc)
	automationCapabilitySvc.SetGitHubConnectionProvider(githubSvc)
	llmSvc.SetGitHubIssueRuntimeProvider(githubSvc)
	llmSvc.SetGitHubAuthRepo(githubAuthRepo)
	llmSvc.SetTaskPullRequestRepo(taskPullRequestRepo)
	llmSvc.SetGitHubPRFeedbackRepo(githubPRFeedbackRepo)
	slackSvc := service.NewSlackService(
		settingsRepo,
		projectRepo,
		llmConfigRepo,
		taskRepo,
		execRepo,
		scheduleRepo,
		taskSvc,
		llmSvc,
		workerSvc,
		slackUserProjectRepo,
		slackTaskContextRepo,
		slackAuthRepo,
	)
	slackSvc.SetCustomPersonalityRepo(customPersonalityRepo)
	slackSvc.SetUploadsDir(uploadsPath)
	slackSvc.SetChatAttachmentRepo(chatAttachmentRepo)
	slackSvc.SetChatBroadcaster(chatBroadcaster)
	slackSvc.SetExecutionStreamHub(executionStreamHub)
	slackSvc.SetAlertService(alertSvc)
	slackSvc.SetTaskGoalService(taskGoalSvc)
	slackSvc.SetThreadInputRepo(repository.NewThreadInputRepo(db))
	slackSvc.SetSlackInboundReceiptRepo(repository.NewSlackInboundReceiptRepo(db))
	slackSvc.SetAgentRepo(agentRepo)
	emailSvc := service.NewEmailService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, emailAuthRepo, emailTaskContextRepo)
	channelMessageRouter := service.NewChannelMessageRouter(channelTargetRepo, settingsRepo)
	llmSvc.SetChannelMessageRouter(channelMessageRouter)
	channelMessageRouter.SetSlackService(slackSvc)
	channelMessageRouter.SetSlackAuthStore(slackAuthRepo)
	channelMessageRouter.SetTelegramAuthStore(telegramAuthRepo)
	channelMessageRouter.SetEmailService(emailSvc)
	channelMessageRouter.SetEmailAuthStore(emailAuthRepo)
	slackSvc.SetChannelMessageRouter(channelMessageRouter)
	emailSvc.SetChannelMessageRouter(channelMessageRouter)
	emailSvc.SetCustomPersonalityRepo(customPersonalityRepo)
	emailSvc.SetUploadsDir(uploadsPath)
	emailSvc.SetChatAttachmentRepo(chatAttachmentRepo)
	emailSvc.SetChatBroadcaster(chatBroadcaster)
	emailSvc.SetExecutionStreamHub(executionStreamHub)
	emailSvc.SetThreadInputRepo(repository.NewThreadInputRepo(db))
	emailSvc.SetEmailInboundReceiptRepo(repository.NewEmailInboundReceiptRepo(db))
	emailSvc.SetAgentRepo(agentRepo)
	discordSvc := service.NewDiscordService(
		settingsRepo,
		projectRepo,
		llmConfigRepo,
		taskRepo,
		execRepo,
		scheduleRepo,
		taskSvc,
		llmSvc,
		workerSvc,
		discordAuthRepo,
		discordTaskContextRepo,
	)
	discordSvc.SetCustomPersonalityRepo(customPersonalityRepo)
	discordSvc.SetUploadsDir(uploadsPath)
	discordSvc.SetChatAttachmentRepo(chatAttachmentRepo)
	discordSvc.SetChatBroadcaster(chatBroadcaster)
	discordSvc.SetExecutionStreamHub(executionStreamHub)
	discordSvc.SetAlertService(alertSvc)
	discordSvc.SetTaskGoalService(taskGoalSvc)
	discordSvc.SetThreadInputRepo(repository.NewThreadInputRepo(db))
	discordSvc.SetAgentRepo(agentRepo)
	discordSvc.SetChannelMessageRouter(channelMessageRouter)
	discordSvc.SetDiscordUserProjectRepo(discordUserProjectRepo)
	channelMessageRouter.SetDiscordService(discordSvc)
	channelMessageRouter.SetDiscordAuthStore(discordAuthRepo)

	// Git worktree service for task isolation
	worktreeSvc := service.NewWorktreeService(taskRepo, projectRepo, settingsRepo)
	llmSvc.SetWorktreeService(worktreeSvc)
	worktreeSvc.SetLLMService(llmSvc)
	schedulerSvc.SetWorktreeService(worktreeSvc)

	// Lifecycle runner: dispatches route_task/before_run/after_complete hook
	// slots around normal task execution. Scheduled agent maintenance uses the
	// existing task scheduler, not a second lifecycle scheduler.
	lifecycleRepo := repository.NewLifecycleRepo(db)
	mutationRepo := repository.NewAgentMutationRepo(db)

	// Auto-memory subsystem. Each project stores managed memory under its configured
	// local repo path; Memory Curator lifecycle hooks select and update task-turn
	// memory, while this service owns storage setup and scheduled consolidation tasks.
	memoryResolver, mrErr := memory.NewPathResolver("", "")
	if mrErr != nil {
		applog.Infof("[memory] path resolver init failed (memory subsystem disabled): %v", mrErr)
	}
	var memorySvc *service.MemoryService
	if memoryResolver != nil {
		memoryStore := memory.NewFileStore(memoryResolver)
		memorySvc = service.NewMemoryService(taskRepo, scheduleRepo, agentRepo, projectRepo, memoryStore, memoryResolver)
		memorySvc.SetLifecycleRepo(lifecycleRepo)
		if err := memorySvc.EnsureGlobalAgents(context.Background()); err != nil {
			applog.Infof("[memory] ensure global system agents: %v", err)
		}
		// Seed per-project memory state for any existing projects. The visible
		// scheduled task is reconciled even for the default project before it has a
		// local repo_path; repo-local memory files remain strict and are prepared only
		// when a project has a configured local repository.
		if existing, lerr := projectRepo.List(context.Background()); lerr == nil {
			for _, p := range existing {
				if err := memorySvc.EnsureProjectSchedules(context.Background(), p.ID); err != nil {
					applog.Infof("[memory] ensure project schedule %s: %v", p.ID, err)
				}
				if strings.TrimSpace(p.RepoPath) == "" {
					continue
				}
				if err := memorySvc.EnsureProject(context.Background(), p.ID); err != nil {
					applog.Infof("[memory] ensure project %s: %v", p.ID, err)
				}
			}
		}
		applog.Infof("[memory] repo-local memory enabled")
	}
	globalSkillRoot := cfg.AppDataDir
	if err := builtinskills.SyncTo(globalSkillRoot); err != nil {
		applog.Infof("warning: failed to sync built-in lifecycle skills: %v", err)
	}
	if err := agentskills.EnsureAgentsRoot(globalSkillRoot); err != nil {
		applog.Infof("warning: failed to ensure agents root at %s: %v", globalSkillRoot, err)
	}
	if err := agentskills.EnsureSkillsRoot(globalSkillRoot); err != nil {
		applog.Infof("warning: failed to ensure skills root at %s: %v", globalSkillRoot, err)
	}
	llmHookInvoker := lifecycle.NewLLMHookInvoker(llmSvc, agentRepo, llmConfigRepo)
	skillResolver := service.NewCatalogSkillResolver(agentRepo, workerSvc.CurrentLifecycleCatalog, globalSkillRoot, func(ctx context.Context, projectID string) string {
		return service.ProjectSkillRootForResolver(ctx, projectRepo, projectID)
	})
	lifecycleRunner := lifecycle.NewRunner(lifecycleRepo, llmHookInvoker, skillResolver)
	workerSvc.SetLifecycleRunner(lifecycleRunner)
	workerSvc.SetLifecycleSkillRoot(globalSkillRoot)
	// Give the LLM service the same root used for global skill catalog and
	// skill mutation writes.
	llmSvc.SetGlobalSkillRoot(globalSkillRoot)
	llmSvc.SetLifecycleRepo(lifecycleRepo)
	mutationRecorderFactory := func(t models.Task) agentlibrary.MutationRecorder {
		return agentlibrary.NewRepoRecorder(mutationRepo, agentlibrary.MutationActor{
			TaskID:    t.ID,
			TaskRunID: t.ID,
			ProjectID: t.ProjectID,
		})
	}
	llmSvc.SetLifecycleMutationRecorderFactory(mutationRecorderFactory)
	workerSvc.SetLifecycleAgentRepo(agentRepo)
	workerSvc.SetLifecycleRepo(lifecycleRepo)
	workerSvc.SetLifecycleMutationRecorderFactory(mutationRecorderFactory)
	agentLibraryMaintenanceSvc := service.NewAgentLibraryMaintenanceService(taskRepo, scheduleRepo, agentRepo)
	agentLibraryMaintenanceSvc.SetLifecycleRepo(lifecycleRepo)
	agentLibraryMaintenanceSvc.SetAgentsRootPath(globalSkillRoot)
	workerSvc.SetAgentRootSyncService(agentLibraryMaintenanceSvc)
	if err := agentLibraryMaintenanceSvc.EnsureGlobalAgents(context.Background()); err != nil {
		applog.Infof("[agent-library] ensure global system agents: %v", err)
	}
	if err := agentLibraryMaintenanceSvc.SyncRootDeclarations(context.Background(), ""); err != nil {
		applog.Infof("[agent-library] sync root declarations: %v", err)
	}
	if existing, lerr := projectRepo.List(context.Background()); lerr == nil {
		for _, p := range existing {
			if err := agentLibraryMaintenanceSvc.EnsureProject(context.Background(), p.ID); err != nil {
				applog.Infof("[agent-library] ensure project %s: %v", p.ID, err)
			}
		}
	}
	applog.Infof("[lifecycle] runner wired with catalog/tools root=%s", globalSkillRoot)
	// Telegram Bot (optional - starts if token is configured via env or saved in DB)
	telegramToken := cfg.TelegramToken
	if telegramToken == "" {
		// Fall back to token saved via Settings page
		if dbToken, getErr := settingsRepo.Get(context.Background(), service.TelegramSettingBotToken); getErr == nil && dbToken != "" {
			telegramToken = dbToken
		}
	}
	var telegramSvc *service.TelegramService
	if telegramToken != "" {
		var tErr error
		telegramSvc, tErr = service.NewTelegramService(
			telegramToken,
			taskSvc,
			projectRepo,
			llmConfigRepo,
			taskRepo,
			execRepo,
			scheduleRepo,
			chatAttachmentRepo,
			llmSvc,
			workerSvc,
		)
		if tErr != nil {
			applog.Infof("warning: failed to initialize telegram bot: %v", tErr)
		} else {
			telegramSvc.SetTelegramAuthRepo(telegramAuthRepo)
			telegramSvc.SetTelegramUserProjectRepo(telegramUserProjectRepo)
			telegramSvc.SetSettingsRepo(settingsRepo)
			telegramSvc.SetCustomPersonalityRepo(customPersonalityRepo)
			telegramSvc.SetAlertService(alertSvc)
			telegramSvc.SetTaskGoalService(taskGoalSvc)
			telegramSvc.SetThreadInputRepo(repository.NewThreadInputRepo(db))
			telegramSvc.SetChannelMessageRouter(channelMessageRouter)
			channelMessageRouter.SetTelegramService(telegramSvc)
			applog.Infof("telegram bot initialized")
		}
	} else {
		applog.Infof("telegram bot disabled (no token configured)")
	}

	// Validate project repo paths exist on disk (catches ephemeral path loss after container restart)
	if missing := projectSvc.ValidateRepoPaths(context.Background()); len(missing) > 0 {
		applog.Infof("WARNING: %d project(s) have missing repo paths — tasks using these projects will fail until repos are restored. Ensure PROJECT_REPO_ROOT is on a persistent volume (e.g. /data/repos).", len(missing))
	}

	// Reset any tasks orphaned in 'running' state from a previous crash, then
	// terminalize their pre-restart running executions. No task workers survive a
	// process restart, so any non-chat execution still marked running is stale.
	if count, resetErr := taskRepo.ResetOrphanedRunning(context.Background()); resetErr != nil {
		applog.Infof("warning: failed to reset orphaned running tasks: %v", resetErr)
	} else if count > 0 {
		applog.Infof("reset %d orphaned running tasks to pending", count)
	}
	if recovered, recoverErr := execRepo.RecoverPreRestartRunningTaskExecutions(context.Background()); recoverErr != nil {
		applog.Infof("warning: failed to recover interrupted task-thread executions: %v", recoverErr)
	} else if recovered > 0 {
		applog.Infof("recovered %d interrupted task-thread execution(s)", recovered)
	}
	if recovered, recoverErr := execRepo.RecoverStaleRunningTaskExecutions(context.Background()); recoverErr != nil {
		applog.Infof("warning: failed to recover stale running task executions: %v", recoverErr)
	} else if recovered > 0 {
		applog.Infof("recovered %d stale running task execution(s)", recovered)
	}

	// Clean up orphaned attachment files
	if count, cleanErr := attachmentRepo.CleanupOrphanedFiles(context.Background(), filepath.Join(cfg.AppDataDir, "uploads")); cleanErr != nil {
		applog.Infof("warning: failed to cleanup orphaned attachments: %v", cleanErr)
	} else if count > 0 {
		applog.Infof("cleaned up %d orphaned attachment files", count)
	}

	// Clean up orphaned chat attachment files
	if count, cleanErr := chatAttachmentRepo.CleanupOrphanedFiles(context.Background(), filepath.Join(cfg.AppDataDir, "uploads")); cleanErr != nil {
		applog.Infof("warning: failed to cleanup orphaned chat attachments: %v", cleanErr)
	} else if count > 0 {
		applog.Infof("cleaned up %d orphaned chat attachment files", count)
	}

	workDir, wdErr := os.Getwd()
	if wdErr != nil || workDir == "" {
		workDir = "."
	}
	mcpBootCtx, mcpBootCancel := context.WithTimeout(context.Background(), 45*time.Second)
	if mcpErr := agentplugins.EnsureInstalledPluginMCPRunning(mcpBootCtx, workDir); mcpErr != nil {
		applog.Infof("warning: persistent plugin MCP startup incomplete: %v", mcpErr)
		alertCtx, alertCancel := context.WithTimeout(context.Background(), 5*time.Second)
		projects, projectErr := projectRepo.List(alertCtx)
		if projectErr != nil {
			applog.Infof("warning: could not load project for MCP startup alert: %v", projectErr)
		} else if len(projects) > 0 {
			a := &models.Alert{
				ProjectID: projects[0].ID,
				Type:      models.AlertCustom,
				Severity:  models.SeverityError,
				Title:     "Plugin MCP startup warning",
				Message:   mcpErr.Error(),
			}
			if alertErr := alertSvc.Create(alertCtx, a); alertErr != nil {
				applog.Infof("warning: failed to create MCP startup alert: %v", alertErr)
			}
		}
		alertCancel()
	}
	mcpBootCancel()

	// Start background services
	srvCtx, srvCancel := context.WithCancel(ctx)
	var hostedPendingStore *auth.PendingStore
	if cfg.AuthMode == auth.AuthModeHostedSSO {
		hostedPendingStore = auth.NewPendingStore(srvCtx, time.Now)
	}

	updateDrain.StartExpirySupervisor(srvCtx)
	workerSvc.Start(srvCtx)
	automationReconciler.Start(srvCtx)
	automationDispatcher.Start(srvCtx)
	startUpdateCoordinator(srvCtx, updateCoordinator)
	if hostedUpdateController != nil {
		hostedUpdateController.Start(srvCtx)
	}

	// HTTP Server
	e := echo.New()
	e.HideBanner = true
	e.Use(middleware.LoggerWithConfig(requestLoggerConfig(nil)))
	e.Use(middleware.Recover())

	// Handle PUT/PATCH/DELETE via form _method field, except on exact
	// authentication protocol routes where the original method is security-sensitive.
	configureMethodOverride(e)

	// Prevent browser from serving cached HTMX partials when a full page is needed
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Response().Header().Set("Vary", "HX-Request")
			if c.Request().Header.Get("HX-Request") == "true" {
				c.Response().Header().Set("Cache-Control", "no-store")
			}
			return next(c)
		}
	})

	h := handler.New(
		projectSvc, taskSvc, llmSvc, workerSvc, schedulerSvc, alertSvc, upcomingSvc, workflowSvc, collisionSvc, insightsSvc, architectSvc, backlogSvc, autonomousTriggerSvc, trendSvc, templateSvc, patternSvc,
		llmConfigRepo, taskRepo, scheduleRepo, execRepo, workerRepo, attachmentRepo, chatAttachmentRepo, projectRepo, settingsRepo, broadcaster, telegramSvc,
	)
	h.SetChatBroadcaster(chatBroadcaster)
	h.SetExecutionStreamHub(executionStreamHub)
	h.SetTaskGoalService(taskGoalSvc)
	h.SetAutomationServices(automationGraphSvc, automationRegistrationSvc)
	h.SetAutomationExternalStateService(automationExternalStateSvc)
	h.SetAutomationBuilderServices(automationDraftSvc, automationCapabilitySvc, automationSaveValidator, automationCompiler, automationConfirmationSvc, automationLifecycleSvc)
	workerSvc.SetAfterCompleteRuntimeToolProvider(h.GoalAgentAfterCompleteRuntimeTools)
	h.SetFileChangeBroadcaster(fileChangeBroadcaster)
	h.SetTelegramAuthRepo(telegramAuthRepo)
	h.SetSlackAuthRepo(slackAuthRepo)
	h.SetEmailAuthRepo(emailAuthRepo)
	h.SetEmailTaskContextRepo(emailTaskContextRepo)
	h.SetEmailService(emailSvc)
	h.SetSlackTaskContextRepo(slackTaskContextRepo)
	h.SetDiscordAuthRepo(discordAuthRepo)
	h.SetDiscordTaskContextRepo(discordTaskContextRepo)
	h.SetReviewCommentRepo(reviewCommentRepo)
	h.SetCustomPersonalityRepo(customPersonalityRepo)
	h.SetWorktreeService(worktreeSvc)
	h.SetAgentRepo(agentRepo)
	h.SetLifecycleRepo(lifecycleRepo)
	h.SetAgentSkillRoot(globalSkillRoot)
	h.SetAgentLibraryMaintenanceService(agentLibraryMaintenanceSvc)
	h.SetTaskPullRequestRepo(taskPullRequestRepo)
	h.SetGitHubPRFeedbackRepo(githubPRFeedbackRepo)
	h.SetGitHubAuthRepo(githubAuthRepo)
	h.SetGitHubService(githubSvc)
	h.SetSlackService(slackSvc)
	h.SetDiscordService(discordSvc)
	h.SetChannelMessageRouter(channelMessageRouter)
	h.SetChannelTargetRepo(channelTargetRepo)
	slackSvc.SetQueuedTurnPromoter(h.PromoteQueuedChatInput)
	slackSvc.SetQueuedTaskThreadPromoter(h.PromoteQueuedTaskThreadInput)
	slackSvc.SetChannelChatRunner(h.StartChannelChatRun)
	slackSvc.SetChannelTaskRunner(h.StartChannelTaskRun)
	emailSvc.SetQueuedTurnPromoter(h.PromoteQueuedChatInput)
	emailSvc.SetChannelChatRunner(h.StartChannelChatRun)
	emailSvc.SetEmailSenderProjectRepo(emailSenderProjectRepo)
	discordSvc.SetQueuedTurnPromoter(h.PromoteQueuedChatInput)
	discordSvc.SetQueuedTaskThreadPromoter(h.PromoteQueuedTaskThreadInput)
	discordSvc.SetChannelChatRunner(h.StartChannelChatRun)
	discordSvc.SetChannelTaskRunner(h.StartChannelTaskRun)
	if telegramSvc != nil {
		telegramSvc.SetChannelMessageRouter(channelMessageRouter)
		channelMessageRouter.SetTelegramService(telegramSvc)
		telegramSvc.SetQueuedTurnPromoter(h.PromoteQueuedChatInput)
		telegramSvc.SetQueuedTaskThreadPromoter(h.PromoteQueuedTaskThreadInput)
		telegramSvc.SetChannelChatRunner(h.StartChannelChatRun)
		telegramSvc.SetChannelTaskRunner(h.StartChannelTaskRun)
		telegramSvc.SetChatBroadcaster(chatBroadcaster)
		telegramSvc.SetExecutionStreamHub(executionStreamHub)
		telegramSvc.SetAgentRepo(agentRepo)
	}
	if err := slackSvc.Start(); err != nil {
		applog.Infof("warning: failed to start slack socket mode: %v", err)
	}
	if err := emailSvc.Start(); err != nil {
		applog.Infof("warning: failed to start email polling: %v", err)
	}
	if err := discordSvc.Start(); err != nil {
		applog.Infof("warning: failed to start discord gateway: %v", err)
	}
	// Start Telegram bot if configured after the shared channel runner is wired.
	if telegramSvc != nil {
		telegramSvc.Start()
	}
	h.SetWebhookRepo(webhookRepo)
	if memorySvc != nil {
		h.SetMemoryService(memorySvc)
	}
	h.SetLocalRepoPathEnabled(cfg.EnableLocalRepoPath)
	h.SetDesktopMode(cfg.Mode == config.ModeDesktop)
	h.SetAppBaseURL(cfg.AppBaseURL)
	h.SetAuthMode(cfg.AuthMode)
	h.SetSystemHealth(currentBuild, cfg.UpdateMode, cfg.Distribution, cfg.HostedAgentToken, cfg.DockerAgentToken, databaseSchema, updateDrain)
	h.SetManagedUpdateError(cfg.ManagedUpdateError)
	h.SetUpdateCoordinator(updateCoordinator)
	h.SetUpdateWorkTracker(updateTracker)
	h.SetAuthConfig(authCfg)
	if cfg.AuthMode == auth.AuthModeHostedSSO {
		h.SetHostedSSO(hostedSSOClient, hostedPendingStore, cfg.HostedSSOKey, cfg.HostedSSOInstanceID, cfg.AppBaseURL)
	}
	e.Use(h.AuthMiddleware())
	llmSvc.SetAgentRepo(agentRepo)
	llmSvc.SetFileChangeBroadcaster(fileChangeBroadcaster)
	llmSvc.SetSlackService(slackSvc)
	llmSvc.SetDiscordService(discordSvc)
	if telegramSvc != nil {
		llmSvc.SetTelegramService(telegramSvc)
	}
	go func() {
		for {
			select {
			case <-srvCtx.Done():
				return
			case <-updateDrain.Reopened():
				workerSvc.ResumeDispatch()
				h.RecoverQueuedInputs(srvCtx)
			}
		}
	}()
	h.RecoverQueuedInputs(context.Background())
	// Start scheduler scans only after durable queued Chat and task-thread inputs
	// have been offered for promotion. Repository admission guards remain authoritative
	// if recovery and a later scan overlap.
	schedulerSvc.Start(srvCtx)
	h.RegisterRoutes(e)

	// Bind listener explicitly so we know the actual port before serving.
	addr := fmt.Sprintf(":%s", cfg.Port)
	ln, listenErr := net.Listen("tcp", addr)
	if listenErr != nil {
		srvCancel()
		db.Close()
		return nil, fmt.Errorf("failed to listen on %s: %w", addr, listenErr)
	}

	boundAddr := ln.Addr().String()
	applog.Infof("starting server on %s", boundAddr)

	// Derive a usable base URL.
	host, port, _ := net.SplitHostPort(boundAddr)
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	baseURL := fmt.Sprintf("http://%s:%s", host, port)
	if binaryUpdateInstaller != nil {
		binaryUpdateInstaller.HealthURL = baseURL + "/api/system/health"
		updateCoordinator.StartRecovery(srvCtx)
	}

	shutdownOnce := make(chan struct{})
	shutdownDone := make(chan struct{})
	shutdownFn := func() {
		select {
		case <-shutdownOnce:
			// Already called — wait for completion.
			<-shutdownDone
			return
		default:
		}
		close(shutdownOnce)
		applog.Infof("shutting down...")
		srvCancel()
		if hostedPendingStore != nil {
			hostedPendingStore.Close()
		}
		schedulerSvc.Stop()
		automationDispatcher.Stop()
		automationReconciler.Stop()
		workerSvc.Stop()
		if telegramSvc != nil {
			telegramSvc.Stop()
		}
		if slackSvc != nil {
			slackSvc.Stop()
		}
		if emailSvc != nil {
			emailSvc.Stop()
		}
		if discordSvc != nil {
			discordSvc.Stop()
		}
		e.Close()
		db.Close()
		close(shutdownDone)
	}

	// Serve in background.
	e.Listener = ln
	go func() {
		if sErr := e.Start(""); sErr != nil {
			applog.Infof("server stopped: %v", sErr)
		}
	}()

	return &Instance{
		BoundAddr:         boundAddr,
		BaseURL:           baseURL,
		Shutdown:          shutdownFn,
		ShutdownRequested: shutdownRequested,
		UpdateCoordinator: updateCoordinator,
	}, nil
}
