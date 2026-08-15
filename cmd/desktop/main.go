// Command desktop launches OpenVibely as a Wails desktop application.
// It starts the shared Go backend on a localhost ephemeral port and loads
// the UI in a native WebView window.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/openvibely/openvibely/internal/applog"
	"strings"
	"sync"

	"github.com/openvibely/openvibely/internal/config"
	"github.com/openvibely/openvibely/internal/server"
	"github.com/openvibely/openvibely/internal/update"
	"github.com/wailsapp/wails/v3/pkg/application"
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
	if len(os.Args) > 1 && os.Args[1] == "desktop-update-helper" {
		cfg, err := update.ParseDesktopHelperArgs(os.Args[2:])
		if err == nil {
			err = update.LoadDesktopHelperRelaunch(os.Stdin, &cfg)
		}
		if err == nil {
			err = update.RunDesktopHelper(context.Background(), cfg)
		}
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

	if err := launch(backend.BaseURL, shutdown, backend.UpdateCoordinator); err != nil {
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
	coordinator.SetDesktopRelaunchContext(baseURL+"/api/system/health", os.Args, workingDirectory, app.Quit)
	if err := coordinator.BindWailsUpdater(app.Updater); err != nil {
		return fmt.Errorf("configure Wails updater: %w", err)
	}

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "main",
		Title:     "OpenVibely",
		URL:       baseURL,
		Width:     1280,
		Height:    820,
		MinWidth:  1024,
		MinHeight: 680,
	})

	return app.Run()
}
