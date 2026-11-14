//go:build linux

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func promptly(t *testing.T, run func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- run() }()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("file operation blocked on a special file")
		return nil
	}
}

func TestNativeFilesRejectAndSkipNamedPipes(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "Files"), 0700); err != nil {
		t.Fatal(err)
	}
	pipe := filepath.Join(dir, "Files", "pipe")
	if err := syscall.Mkfifo(pipe, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("pipe", filepath.Join(dir, "Files", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Files", "normal.txt"), []byte("needle"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{"pipe", "link"} {
		for _, operation := range []string{"read_file", "edit_file", "write_file"} {
			t.Run(operation+"/"+name, func(t *testing.T) {
				p := nativeFileRequest{Operation: operation}
				p.Input.FilePath = name
				p.Input.Content = "replacement"
				p.Input.Old = "needle"
				p.Input.New = "updated"
				if err := promptly(t, func() error { _, err := nativeFileOperation(context.Background(), root, p); return err }); err == nil {
					t.Fatal("accepted special file")
				}
			})
		}
	}
	for _, operation := range []string{"grep_search", "list_files"} {
		t.Run(operation, func(t *testing.T) {
			p := nativeFileRequest{Operation: operation}
			if operation == "grep_search" {
				p.Input.Pattern = "needle"
			} else {
				p.Input.Pattern = "*.txt"
			}
			p.Input.Recursive = true
			var out string
			err := promptly(t, func() error { var err error; out, err = nativeFileOperation(context.Background(), root, p); return err })
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "normal.txt") || strings.Contains(out, "pipe") || strings.Contains(out, "link") {
				t.Fatalf("unexpected output: %s", out)
			}
		})
	}
	info, err := os.Lstat(pipe)
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatal("pipe modified", err)
	}
}

func TestFileTransferRejectsNamedPipe(t *testing.T) {
	// Runtime integration test runs inside the Linux computer container.
	if err := os.MkdirAll(computerHome(), 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(computerHome(), "file-transfer-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	pipe := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(pipe, 0600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	workspaceRoutes(mux)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/files?path="+pipe, strings.NewReader("data"))
			promptly(t, func() error { mux.ServeHTTP(rec, req); return nil })
			if rec.Code < 400 {
				t.Fatalf("special file accepted: %d", rec.Code)
			}
		})
	}
}
