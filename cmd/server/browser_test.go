package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestStartupBrowserWaitsForHealthAndUsesBoundURL(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/system/health" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if requests.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	calls := 0
	err := openStartupBrowser(context.Background(), srv.URL, "", func(target string) error {
		calls++
		if requests.Load() < 3 {
			t.Error("opened before readiness")
		}
		if target != srv.URL+"/chat" {
			t.Errorf("target = %q", target)
		}
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestStartupBrowserDisabled(t *testing.T) {
	for _, value := range []string{"true", "TRUE", "1"} {
		t.Run(value, func(t *testing.T) {
			if err := openStartupBrowser(context.Background(), ":invalid", value, func(string) error {
				t.Fatal("browser opened despite override")
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStartupBrowserCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	err := openStartupBrowser(ctx, srv.URL, "false", func(string) error { t.Fatal("opened unhealthy server"); return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
}

func TestStartupBrowserLaunchFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	want := errors.New("no browser installed")
	err := openStartupBrowser(context.Background(), srv.URL, "false", func(string) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}
