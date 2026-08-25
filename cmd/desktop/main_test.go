package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/config"
	"github.com/openvibely/openvibely/internal/update"
)

// TestEnsureDesktopPATHMakesGoLocatable reproduces the "bash: go: command not
// found" runtime issue: desktop GUI launches can inherit a minimal PATH that
// excludes paths initialized by the user's shell. After EnsureDesktopPATH runs
// at startup, `go` must be locatable when the user's shell adds it to PATH.
func TestEnsureDesktopPATHMakesGoLocatable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell PATH bootstrap is not used on windows")
	}

	tmp := t.TempDir()
	goBin := filepath.Join(tmp, "not-a-standard-location", "go", "bin")
	if err := os.MkdirAll(goBin, 0o755); err != nil {
		t.Fatalf("mkdir fake go bin: %v", err)
	}
	fakeGo := filepath.Join(goBin, "go")
	if err := os.WriteFile(fakeGo, []byte("#!/bin/sh\necho go version test\n"), 0o755); err != nil {
		t.Fatalf("write fake go: %v", err)
	}

	shell := filepath.Join(tmp, "shell")
	script := "#!/bin/sh\nPATH=\"" + goBin + ":$PATH\"\nfor arg do cmd=\"$arg\"; done\nexec /bin/sh -c \"$cmd\"\n"
	if err := os.WriteFile(shell, []byte(script), 0o755); err != nil {
		t.Fatalf("write shell: %v", err)
	}

	// Simulate a Wails GUI launch with a restricted desktop-session PATH.
	// Use temp directories so the test is independent of runner image contents.
	minimalBin := filepath.Join(tmp, "minimal-bin")
	minimalSbin := filepath.Join(tmp, "minimal-sbin")
	if err := os.MkdirAll(minimalBin, 0o755); err != nil {
		t.Fatalf("mkdir minimal bin: %v", err)
	}
	if err := os.MkdirAll(minimalSbin, 0o755); err != nil {
		t.Fatalf("mkdir minimal sbin: %v", err)
	}
	minimal := minimalBin + string(os.PathListSeparator) + minimalSbin
	t.Setenv("PATH", minimal)
	t.Setenv("SHELL", shell)

	if found, err := exec.LookPath("go"); err == nil {
		t.Fatalf("test setup expected `go` to be unavailable before bootstrap, found %q", found)
	}

	// Run the desktop PATH bootstrap exactly as cmd/desktop/main.go does.
	newPATH := config.EnsureDesktopPATH()
	if !strings.HasPrefix(newPATH, goBin+string(os.PathListSeparator)) {
		t.Fatalf("expected EnsureDesktopPATH to use shell PATH with fake go dir first; got %q", newPATH)
	}
	if found, err := exec.LookPath("go"); err != nil {
		t.Fatalf("after EnsureDesktopPATH, expected `go` to be locatable; PATH=%q err=%v", os.Getenv("PATH"), err)
	} else if found != fakeGo {
		t.Fatalf("expected fake go at %q, got %q", fakeGo, found)
	}
}

func TestEnsureDesktopPluginRootUsesExternalApplicationData(t *testing.T) {
	t.Setenv("OPENVIBELY_PLUGIN_ROOT", "")
	appData := filepath.Join(t.TempDir(), "OpenVibely Data")
	if err := ensureDesktopPluginRoot(&config.Config{AppDataDir: appData}); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(appData, ".openvibely", "plugins")
	if got := os.Getenv("OPENVIBELY_PLUGIN_ROOT"); got != want {
		t.Fatalf("desktop plugin root = %q, want %q", got, want)
	}
}

func TestDesktopCommandRoutesPackagedUpdateHelpers(t *testing.T) {
	tests := []struct {
		name          string
		command       string
		handled       bool
		errorContains string
	}{
		{name: "app-bundle update helper", command: update.AppBundleUpdateHelperCommand, handled: true, errorContains: "invalid app-bundle-update-helper arguments"},
		{name: "executable update helper", command: update.ExecutableUpdateHelperCommand, handled: true, errorContains: "unsupported executable-update-helper argument"},
		{name: "normal desktop launch", command: "serve", handled: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handled, err := runPackagedUpdateHelperCommand(context.Background(), []string{"openvibely-desktop", test.command, "--unsupported", "value"}, strings.NewReader(""))
			if handled != test.handled {
				t.Fatalf("handled = %t, want %t", handled, test.handled)
			}
			if test.handled && err == nil {
				t.Fatal("helper command unexpectedly accepted invalid arguments")
			}
			if test.errorContains != "" && !strings.Contains(err.Error(), test.errorContains) {
				t.Fatalf("helper command error = %q, want it to contain %q", err, test.errorContains)
			}
			if !test.handled && err != nil {
				t.Fatalf("normal desktop command returned error: %v", err)
			}
		})
	}
}

func TestDesktopPackagedUpdateHelperIntegrationTimeouts(t *testing.T) {
	t.Setenv("OPENVIBELY_UPDATE_INTEGRATION_WAIT_TIMEOUT_MS", "2500")
	t.Setenv("OPENVIBELY_UPDATE_INTEGRATION_VALIDATION_TIMEOUT_MS", "7500")
	var executableCfg update.ExecutableUpdateHelperConfig
	if err := applyUpdateIntegrationTimeouts(&executableCfg); err != nil {
		t.Fatal(err)
	}
	if executableCfg.WaitTimeout != 2500*time.Millisecond {
		t.Fatalf("executable wait timeout = %s", executableCfg.WaitTimeout)
	}
	if executableCfg.ValidationTimeout != 7500*time.Millisecond {
		t.Fatalf("executable validation timeout = %s", executableCfg.ValidationTimeout)
	}
	var appBundleCfg update.AppBundleUpdateHelperConfig
	if err := applyAppBundleUpdateIntegrationTimeouts(&appBundleCfg); err != nil {
		t.Fatal(err)
	}
	if appBundleCfg.WaitTimeout != 2500*time.Millisecond {
		t.Fatalf("app-bundle wait timeout = %s", appBundleCfg.WaitTimeout)
	}
	if appBundleCfg.ValidationTimeout != 7500*time.Millisecond {
		t.Fatalf("app-bundle validation timeout = %s", appBundleCfg.ValidationTimeout)
	}
}

func TestDesktopWindowUsesEphemeralPortWebViewWithPersistentStorage(t *testing.T) {
	content, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read desktop main.go: %v", err)
	}
	source := string(content)
	if !strings.Contains(source, `application.WebviewWindowOptions{`) || !strings.Contains(source, `URL:       baseURL,`) {
		t.Fatal("desktop launcher must keep loading the server UI through a Wails WebView window")
	}
	for _, disallowed := range []string{`DataPath:`, `PrivateMode`, `Incognito`, `Ephemeral`, `ClearBrowsingData`} {
		if strings.Contains(source, disallowed) {
			t.Fatalf("desktop launcher appears to override persistent WebView storage with %s", disallowed)
		}
	}
}

func TestRunDesktopLaunchesNativeWindow(t *testing.T) {
	cfg := &config.Config{Mode: config.ModeDesktop}

	started := false
	launched := false
	shutdownCalled := false
	launchedURL := ""

	err := runDesktop(
		cfg,
		func(context.Context, *config.Config) (*desktopBackend, error) {
			started = true
			return &desktopBackend{
				BaseURL: "http://127.0.0.1:43210",
				Shutdown: func() {
					shutdownCalled = true
				},
			}, nil
		},
		func(url string, onShutdown func(), _ *update.Coordinator) error {
			launched = true
			launchedURL = url
			onShutdown()
			return nil
		},
	)
	if err != nil {
		t.Fatalf("runDesktop returned error: %v", err)
	}
	if !started {
		t.Fatalf("expected backend start to be called")
	}
	if !launched {
		t.Fatalf("expected native window launcher to be called")
	}
	if launchedURL != "http://127.0.0.1:43210" {
		t.Fatalf("expected launcher URL to be backend base URL, got %q", launchedURL)
	}
	if !shutdownCalled {
		t.Fatalf("expected backend shutdown to be called")
	}
}

func TestRunDesktopStartFailure(t *testing.T) {
	cfg := &config.Config{Mode: config.ModeDesktop}

	startErr := errors.New("boom")
	launched := false

	err := runDesktop(
		cfg,
		func(context.Context, *config.Config) (*desktopBackend, error) {
			return nil, startErr
		},
		func(string, func(), *update.Coordinator) error {
			launched = true
			return nil
		},
	)
	if !errors.Is(err, startErr) {
		t.Fatalf("expected start error, got %v", err)
	}
	if launched {
		t.Fatalf("expected launcher not to run when backend fails")
	}
}

func TestRunDesktopIgnoresNativeWindowErrorAfterUpdaterShutdown(t *testing.T) {
	cfg := &config.Config{Mode: config.ModeDesktop}
	launchErr := errors.New("webview stopped during initialization")
	shutdownCalled := false

	err := runDesktop(
		cfg,
		func(context.Context, *config.Config) (*desktopBackend, error) {
			return &desktopBackend{
				BaseURL: "http://127.0.0.1:43210",
				Shutdown: func() {
					shutdownCalled = true
				},
			}, nil
		},
		func(_ string, onShutdown func(), _ *update.Coordinator) error {
			onShutdown()
			return launchErr
		},
	)
	if err != nil {
		t.Fatalf("runDesktop returned an error after updater shutdown: %v", err)
	}
	if !shutdownCalled {
		t.Fatal("expected backend shutdown to be called")
	}
}

func TestRunDesktopReturnsNativeWindowErrorWithoutShutdown(t *testing.T) {
	cfg := &config.Config{Mode: config.ModeDesktop}
	launchErr := errors.New("webview failed")

	err := runDesktop(
		cfg,
		func(context.Context, *config.Config) (*desktopBackend, error) {
			return &desktopBackend{BaseURL: "http://127.0.0.1:43210"}, nil
		},
		func(string, func(), *update.Coordinator) error {
			return launchErr
		},
	)
	if !errors.Is(err, launchErr) {
		t.Fatalf("runDesktop error = %v, want native window error", err)
	}
}

func TestLoadDesktopConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.env"
	if err := os.WriteFile(path, []byte("OPENVIBELY_APP_DATA_DIR=/tmp/shared-openvibely\n"), 0o644); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	t.Setenv("OPENVIBELY_DESKTOP_CONFIG_FILE", path)
	unsetEnv(t, "OPENVIBELY_APP_DATA_DIR")

	loadDesktopConfigFile()

	if got := os.Getenv("OPENVIBELY_APP_DATA_DIR"); got != "/tmp/shared-openvibely" {
		t.Fatalf("OPENVIBELY_APP_DATA_DIR=%q", got)
	}
}

func TestSetDesktopOAuthDefaults(t *testing.T) {
	t.Run("defaults oauth redirect mode to auto when unset", func(t *testing.T) {
		unsetEnv(t, "OAUTH_REDIRECT_MODE")
		unsetEnv(t, "OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS")

		setDesktopOAuthDefaults()

		if got := os.Getenv("OAUTH_REDIRECT_MODE"); got != "auto" {
			t.Fatalf("expected OAUTH_REDIRECT_MODE=auto, got %q", got)
		}
		if got := os.Getenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS"); got != "true" {
			t.Fatalf("expected desktop private model endpoints to be enabled, got %q", got)
		}
	})

	t.Run("does not override explicitly configured oauth redirect mode", func(t *testing.T) {
		t.Setenv("OAUTH_REDIRECT_MODE", "hosted")
		t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "false")

		setDesktopOAuthDefaults()

		if got := os.Getenv("OAUTH_REDIRECT_MODE"); got != "hosted" {
			t.Fatalf("expected OAUTH_REDIRECT_MODE to stay hosted, got %q", got)
		}
		if got := os.Getenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS"); got != "false" {
			t.Fatalf("expected explicit private endpoint policy to remain false, got %q", got)
		}
	})
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	if had {
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("failed to unset %s: %v", key, err)
		}
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
			return
		}
		_ = os.Unsetenv(key)
	})
}
