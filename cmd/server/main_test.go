package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/update"
)

func TestApplyUpdateIntegrationTimeouts(t *testing.T) {
	t.Setenv("OPENVIBELY_UPDATE_INTEGRATION_WAIT_TIMEOUT_MS", "2500")
	t.Setenv("OPENVIBELY_UPDATE_INTEGRATION_VALIDATION_TIMEOUT_MS", "7500")
	cfg := update.ExecutableUpdateHelperConfig{
		WaitTimeout:       11 * time.Second,
		ValidationTimeout: 22 * time.Second,
	}

	applyUpdateIntegrationTimeouts(&cfg)

	if cfg.WaitTimeout != 2500*time.Millisecond {
		t.Fatalf("wait timeout = %s, want %s", cfg.WaitTimeout, 2500*time.Millisecond)
	}
	if cfg.ValidationTimeout != 7500*time.Millisecond {
		t.Fatalf("validation timeout = %s, want %s", cfg.ValidationTimeout, 7500*time.Millisecond)
	}
}

func TestWaitForShutdownAcceptsBinaryUpdateHandoff(t *testing.T) {
	requested := make(chan struct{})
	close(requested)
	done := make(chan struct{})
	go func() {
		waitForShutdown(make(chan os.Signal), requested)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("binary update shutdown handoff did not stop command wait")
	}
}

func TestRunHealthcheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/system/health" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := runHealthcheck(srv.URL+"/api/system/health", srv.Client()); err != nil {
		t.Fatal(err)
	}
}
