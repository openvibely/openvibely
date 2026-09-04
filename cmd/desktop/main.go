// Command desktop launches OpenVibely as a Wails desktop application.
// It starts the shared Go backend on a localhost ephemeral port and loads
// the UI in a native WebView window.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/config"
	"github.com/openvibely/openvibely/internal/server"
	"github.com/openvibely/openvibely/internal/update"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	desktopicons "github.com/openvibely/openvibely/assets/desktop/icons"
)

type desktopBackend struct {
	BaseURL           string
	Shutdown          func()
	UpdateCoordinator *update.Coordinator
}

type desktopStarter func(context.Context, *config.Config) (*desktopBackend, error)
type desktopLauncher func(baseURL string, onShutdown func(), coordinator *update.Coordinator) error

func main() {
	log.SetOutput(os.Stderr)
	if handled, err := runPackagedUpdateHelperCommand(context.Background(), os.Args, os.Stdin); handled {
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	setDesktopOAuthDefaults()
	loadDesktopConfigFile()

	// GUI launches often inherit a minimal desktop-session PATH instead of the
	// user's shell-initialized PATH. Merge the user's real shell PATH once here
	// so every subprocess (task shells, plugin MCP servers, etc.) inherits it.
	config.EnsureDesktopPATH()
	applog.Infof("[desktop] initialized task PATH from user shell")

	cfg := config.LoadWithMode(config.ModeDesktop)

	applog.Infof("[desktop] starting OpenVibely desktop app...")

	if err := runDesktop(cfg, startDesktopBackend, launchNativeWindow); err != nil {
		log.Fatalf("[desktop] failed: %v", err)
	}
}

func runPackagedUpdateHelperCommand(ctx context.Context, args []string, stdin io.Reader) (bool, error) {
	if len(args) < 2 {
		return false, nil
	}
	switch args[1] {
	case update.AppBundleUpdateHelperCommand:
		cfg, err := update.ParseAppBundleUpdateHelperArgs(args[2:])
		if err == nil {
			err = update.LoadAppBundleUpdateHelperRelaunch(stdin, &cfg)
		}
		if err == nil {
			err = applyAppBundleUpdateIntegrationTimeouts(&cfg)
		}
		if err == nil {
			err = update.RunAppBundleUpdateHelper(ctx, cfg)
		}
		return true, err
	case update.ExecutableUpdateHelperCommand:
		cfg, err := update.ParseExecutableUpdateHelperArgs(args[2:])
		if err == nil {
			if cfg.RelaunchMetadataPath != "" {
				err = update.LoadExecutableUpdateHelperRelaunchFile(cfg.RelaunchMetadataPath, &cfg)
			} else {
				err = update.LoadExecutableUpdateHelperRelaunch(stdin, &cfg)
			}
		}
		if err == nil {
			err = applyUpdateIntegrationTimeouts(&cfg)
		}
		if err == nil {
			err = update.RunExecutableUpdateHelper(ctx, cfg)
		}
		return true, err
	default:
		return false, nil
	}
}

func applyUpdateIntegrationTimeouts(cfg *update.ExecutableUpdateHelperConfig) error {
	if cfg == nil {
		return nil
	}
	return applyUpdateIntegrationTimeoutValues(&cfg.WaitTimeout, &cfg.ValidationTimeout)
}

func applyAppBundleUpdateIntegrationTimeouts(cfg *update.AppBundleUpdateHelperConfig) error {
	if cfg == nil {
		return nil
	}
	return applyUpdateIntegrationTimeoutValues(&cfg.WaitTimeout, &cfg.ValidationTimeout)
}

func applyUpdateIntegrationTimeoutValues(waitTimeout, validationTimeout *time.Duration) error {
	if value := os.Getenv("OPENVIBELY_UPDATE_INTEGRATION_WAIT_TIMEOUT_MS"); value != "" {
		milliseconds, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("parse update integration wait timeout: %w", err)
		}
		*waitTimeout = time.Duration(milliseconds) * time.Millisecond
	}
	if value := os.Getenv("OPENVIBELY_UPDATE_INTEGRATION_VALIDATION_TIMEOUT_MS"); value != "" {
		milliseconds, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("parse update integration validation timeout: %w", err)
		}
		*validationTimeout = time.Duration(milliseconds) * time.Millisecond
	}
	return nil
}

func ensureDesktopPluginRoot(cfg *config.Config) error {
	if strings.TrimSpace(os.Getenv("OPENVIBELY_PLUGIN_ROOT")) != "" {
		return nil
	}
	root := filepath.Join(cfg.AppDataDir, ".openvibely", "plugins")
	if err := os.Setenv("OPENVIBELY_PLUGIN_ROOT", root); err != nil {
		return fmt.Errorf("configure desktop plugin data root: %w", err)
	}
	return nil
}

func setDesktopOAuthDefaults() {
	if strings.TrimSpace(os.Getenv("OAUTH_REDIRECT_MODE")) == "" {
		_ = os.Setenv("OAUTH_REDIRECT_MODE", "auto")
	}
	if strings.TrimSpace(os.Getenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS")) == "" {
		_ = os.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	}
}

func loadDesktopConfigFile() {
	path := config.DesktopConfigFilePath()
	if strings.TrimSpace(path) == "" {
		return
	}
	if err := config.LoadEnvFile(path); err != nil {
		if os.IsNotExist(err) {
			applog.Infof("[desktop] config file not found at %s; using defaults", path)
			return
		}
		applog.Infof("[desktop] error loading config file %s: %v", path, err)
		return
	}
	applog.Infof("[desktop] loaded config file %s", path)
}

func runDesktop(cfg *config.Config, start desktopStarter, launch desktopLauncher) error {
	if cfg == nil {
		return fmt.Errorf("desktop config is nil")
	}

	if err := ensureDesktopPluginRoot(cfg); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())

	backend, err := start(ctx, cfg)
	if err != nil {
		cancel()
		return fmt.Errorf("failed to start backend: %w", err)
	}

	applog.Infof("[desktop] backend listening at %s", backend.BaseURL)

	var shutdownOnce sync.Once
	shutdown := func() {
		shutdownOnce.Do(func() {
			cancel()
			if backend.Shutdown != nil {
				backend.Shutdown()
			}
		})
	}

	if os.Getenv("OPENVIBELY_UPDATE_E2E_HEADLESS_DESKTOP") == "1" && runtime.GOOS != "darwin" {
		if backend.UpdateCoordinator == nil {
			shutdown()
			return fmt.Errorf("desktop update coordinator is unavailable")
		}
		workingDirectory, err := os.Getwd()
		if err != nil {
			shutdown()
			return fmt.Errorf("resolve desktop working directory: %w", err)
		}
		backend.UpdateCoordinator.SetDesktopRelaunchContext(backend.BaseURL+"/api/system/health", os.Args, workingDirectory, shutdown)
		if err := backend.UpdateCoordinator.BindWailsUpdater(nil); err != nil {
			shutdown()
			return fmt.Errorf("configure desktop updater: %w", err)
		}
		<-ctx.Done()
		applog.Infof("[desktop] shutdown complete")
		return nil
	}

	if err := launch(backend.BaseURL, shutdown, backend.UpdateCoordinator); err != nil {
		if ctx.Err() != nil {
			applog.Infof("[desktop] native window stopped during shutdown: %v", err)
			return nil
		}
		shutdown()
		return fmt.Errorf("failed to launch native desktop window: %w", err)
	}

	shutdown()
	applog.Infof("[desktop] shutdown complete")
	return nil
}

func startDesktopBackend(ctx context.Context, cfg *config.Config) (*desktopBackend, error) {
	inst, err := server.Start(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &desktopBackend{
		BaseURL:           inst.BaseURL,
		Shutdown:          inst.Shutdown,
		UpdateCoordinator: inst.UpdateCoordinator,
	}, nil
}

func launchNativeWindow(baseURL string, onShutdown func(), coordinator *update.Coordinator) error {
	app := application.New(application.Options{
		Name:        "OpenVibely",
		Description: "OpenVibely desktop application",
		Icon:        desktopicons.OpenVibelyPNG,
		OnShutdown:  onShutdown,
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})
	if coordinator == nil {
		return fmt.Errorf("desktop update coordinator is unavailable")
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve desktop working directory: %w", err)
	}
	var updaterErr error
	var updaterErrMu sync.Mutex
	var bindUpdaterOnce sync.Once
	bindUpdater := func() {
		bindUpdaterOnce.Do(func() {
			coordinator.SetDesktopRelaunchContext(baseURL+"/api/system/health", os.Args, workingDirectory, app.Quit)
			if err := coordinator.BindWailsUpdater(app.Updater); err != nil {
				updaterErrMu.Lock()
				updaterErr = err
				updaterErrMu.Unlock()
				app.Quit()
			}
		})
	}
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "main",
		Title:     "OpenVibely",
		URL:       baseURL,
		Width:     1280,
		Height:    820,
		MinWidth:  1024,
		MinHeight: 680,
	})
	registerDesktopUpdaterBinding(runtime.GOOS, app.Event.OnApplicationEvent, window.OnWindowEvent, application.InvokeAsync, bindUpdater)

	if err := app.Run(); err != nil {
		return err
	}
	updaterErrMu.Lock()
	defer updaterErrMu.Unlock()
	if updaterErr != nil {
		return fmt.Errorf("configure Wails updater: %w", updaterErr)
	}
	return nil
}

func registerDesktopUpdaterBinding(
	goos string,
	onApplicationEvent func(events.ApplicationEventType, func(*application.ApplicationEvent)) func(),
	onWindowEvent func(events.WindowEventType, func(*application.WindowEvent)) func(),
	invokeAfterNativeEvent func(func()),
	bind func(),
) {
	if goos == "windows" {
		onWindowEvent(events.Windows.WebViewNavigationCompleted, func(*application.WindowEvent) {
			invokeAfterNativeEvent(bind)
		})
		return
	}
	onApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		bind()
	})
}
