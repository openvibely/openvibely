//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDesktopHealthWithoutBrowser(t *testing.T) {
	// Reserve a contiguous set of desktop ports, leaving the CDP port closed.
	var listeners []net.Listener
	var base int
	for attempt := 0; attempt < 100; attempt++ {
		first, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		base = first.Addr().(*net.TCPAddr).Port - 1
		listeners = []net.Listener{first}
		for offset := 2; offset <= 5; offset++ {
			l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(base+offset))
			if err != nil {
				break
			}
			listeners = append(listeners, l)
		}
		if len(listeners) == 5 {
			break
		}
		for _, l := range listeners {
			l.Close()
		}
		listeners = nil
	}
	if len(listeners) != 5 {
		t.Fatal("could not reserve desktop ports")
	}
	defer func() {
		for _, l := range listeners {
			l.Close()
		}
	}()
	listeners[0].Close()
	t.Setenv("OPENVIBELY_DESKTOP_PORT", strconv.Itoa(base))
	handler := desktopServer()
	check := func(want int) {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
		if w.Code != want {
			t.Fatalf("health = %d: %s", w.Code, w.Body.String())
		}
	}
	check(http.StatusOK)
	listeners[1].Close()
	check(http.StatusServiceUnavailable)
}

func TestBrowserReadinessWait(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	if err := waitForBrowser(t.Context(), server.URL); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("attempts = %d", calls.Load())
	}
}

func TestBrowserReadinessTimeoutAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	start := time.Now()
	if err := waitForBrowser(t.Context(), server.URL); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 10*time.Second || elapsed > 12*time.Second {
		t.Fatalf("wait took %s", elapsed)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := waitForBrowser(ctx, server.URL); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestBrowserUnavailableDoesNotStartWorker(t *testing.T) {
	previous := awaitBrowserReady
	awaitBrowserReady = func(context.Context, string) error { return context.DeadlineExceeded }
	defer func() { awaitBrowserReady = previous }()
	mux := http.NewServeMux()
	browserRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/browser", strings.NewReader(`{"action":"read"}`)))
	var response struct {
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusBadGateway || response.Error.Code != "browser_unavailable" || !strings.Contains(response.Error.Message, "10 seconds") || currentBrowserWorker != nil {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
	}
}
