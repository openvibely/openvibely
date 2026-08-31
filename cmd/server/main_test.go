package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

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
