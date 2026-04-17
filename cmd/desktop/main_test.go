package main

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/openvibely/openvibely/internal/config"
)

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
		func(url string, onShutdown func()) error {
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
		func(string, func()) error {
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

func TestSetDesktopOAuthDefaults(t *testing.T) {
	t.Run("defaults oauth redirect mode to auto when unset", func(t *testing.T) {
		unsetEnv(t, "OAUTH_REDIRECT_MODE")

		setDesktopOAuthDefaults()

		if got := os.Getenv("OAUTH_REDIRECT_MODE"); got != "auto" {
			t.Fatalf("expected OAUTH_REDIRECT_MODE=auto, got %q", got)
		}
	})

	t.Run("does not override explicitly configured oauth redirect mode", func(t *testing.T) {
		t.Setenv("OAUTH_REDIRECT_MODE", "hosted")

		setDesktopOAuthDefaults()

		if got := os.Getenv("OAUTH_REDIRECT_MODE"); got != "hosted" {
			t.Fatalf("expected OAUTH_REDIRECT_MODE to stay hosted, got %q", got)
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
