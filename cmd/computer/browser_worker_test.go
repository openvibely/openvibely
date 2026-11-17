//go:build linux

package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestBrowserWorkerChild(t *testing.T) {
	if os.Getenv("OV_BROWSER_WORKER_TEST") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "hang") {
			time.Sleep(time.Minute)
		}
		fmt.Println(scanner.Text())
	}
	os.Exit(0)
}
func TestBrowserWorkerRequestsAndCancellation(t *testing.T) {
	previous := newBrowserCommand
	newBrowserCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBrowserWorkerChild$")
		cmd.Env = append(os.Environ(), "OV_BROWSER_WORKER_TEST=1")
		return cmd
	}
	t.Cleanup(func() { stopBrowserWorker(); newBrowserCommand = previous })
	mux := http.NewServeMux()
	browserRoutes(mux)
	for _, body := range []string{"{\n\"action\":\"read\"\n}", `{"action":"tabs"}`} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/browser", strings.NewReader(body)))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "action") {
			t.Fatalf("response %d %s", w.Code, w.Body.String())
		}
	}
	pid := currentBrowserWorker.cmd.Process.Pid
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/browser", strings.NewReader(`{"action":"hang"}`)).WithContext(ctx)
	mux.ServeHTTP(w, req)
	if w.Code != 504 || currentBrowserWorker != nil {
		t.Fatalf("cancel did not stop worker: %d", w.Code)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/browser", strings.NewReader(`{"action":"read"}`)))
	if w.Code != 200 || currentBrowserWorker.cmd.Process.Pid == pid {
		t.Fatal("worker did not recover")
	}
}
