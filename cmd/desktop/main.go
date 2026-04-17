// Command desktop launches OpenVibely as a Wails desktop application.
// It starts the shared Go backend on a localhost ephemeral port and loads
// the UI in a native WebView window.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/openvibely/openvibely/internal/config"
	"github.com/openvibely/openvibely/internal/server"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type desktopBackend struct {
	BaseURL  string
	Shutdown func()
}

type desktopStarter func(context.Context, *config.Config) (*desktopBackend, error)
type desktopLauncher func(baseURL string, onShutdown func()) error

func main() {
	setDesktopOAuthDefaults()

	cfg := config.LoadWithMode(config.ModeDesktop)

	log.SetOutput(os.Stderr)
	log.Println("[desktop] starting OpenVibely desktop app...")

	if err := runDesktop(cfg, startDesktopBackend, launchNativeWindow); err != nil {
		log.Fatalf("[desktop] failed: %v", err)
	}
}

func setDesktopOAuthDefaults() {
	if strings.TrimSpace(os.Getenv("OAUTH_REDIRECT_MODE")) == "" {
		_ = os.Setenv("OAUTH_REDIRECT_MODE", "auto")
	}
}

func runDesktop(cfg *config.Config, start desktopStarter, launch desktopLauncher) error {
	if cfg == nil {
		return fmt.Errorf("desktop config is nil")
	}

	ctx, cancel := context.WithCancel(context.Background())

	backend, err := start(ctx, cfg)
	if err != nil {
		cancel()
		return fmt.Errorf("failed to start backend: %w", err)
	}

	log.Printf("[desktop] backend listening at %s", backend.BaseURL)

	var shutdownOnce sync.Once
	shutdown := func() {
		shutdownOnce.Do(func() {
			cancel()
			if backend.Shutdown != nil {
				backend.Shutdown()
			}
		})
	}

	if err := launch(backend.BaseURL, shutdown); err != nil {
		shutdown()
		return fmt.Errorf("failed to launch native desktop window: %w", err)
	}

	shutdown()
	log.Println("[desktop] shutdown complete")
	return nil
}

func startDesktopBackend(ctx context.Context, cfg *config.Config) (*desktopBackend, error) {
	inst, err := server.Start(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &desktopBackend{
		BaseURL:  inst.BaseURL,
		Shutdown: inst.Shutdown,
	}, nil
}

func launchNativeWindow(baseURL string, onShutdown func()) error {
	app := application.New(application.Options{
		Name:        "OpenVibely",
		Description: "OpenVibely desktop application",
		OnShutdown:  onShutdown,
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

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
