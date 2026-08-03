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
