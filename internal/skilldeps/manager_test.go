package skilldeps

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/testutil/retrytest"
)

func zipTool(t *testing.T, name, body string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	h := &zip.FileHeader{Name: name, Method: zip.Deflate}
	h.SetMode(0755)
	f, err := w.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestInstallConcurrentCacheAndCorruption(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a shell executable")
	}
	data := zipTool(t, "bin/tool", "#!/bin/sh\nexit 0\n")
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write(data)
	}))
	defer server.Close()
	dep := Dependency{Name: "tool", Version: "1.0", HealthCheck: []string{"--version"}, Packages: map[string]Package{"linux/amd64": {URL: server.URL, SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), Format: "zip", Executable: "bin/tool"}}}
	manager := &Manager{CacheDir: t.TempDir(), Platform: "linux/amd64", Client: server.Client()}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := manager.Resolve(retrytest.Context(), []Dependency{dep})
			if err != nil || len(r) != 1 || r[0].Error != "" {
				t.Errorf("resolve: %v %v", r, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 2 {
		t.Fatalf("downloaded %d times", calls.Load())
	}
	server.Close()
	result, err := manager.Resolve(retrytest.Context(), []Dependency{dep})
	if err != nil || result[0].Status != "cached" {
		t.Fatalf("offline cache: %v %v", result, err)
	}
	if err = os.WriteFile(result[0].Executable, []byte("broken"), 0755); err != nil {
		t.Fatal(err)
	}
	// A broken offline cache must fail within the caller's retry budget.
	retryCtx, cancel := context.WithTimeout(retrytest.Context(), 100*time.Millisecond)
	defer cancel()
	result, err = manager.Resolve(retryCtx, []Dependency{dep})
	if err != nil || result[0].Executable != "" || result[0].Error == "" {
		t.Fatalf("used broken cache: %v %v", result, err)
	}
}
func TestUnsupportedAndLocalOverrides(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a shell executable")
	}
	local := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(local, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	deps := []Dependency{{Name: "tool", Version: "development", HealthCheck: []string{"version"}}, {Name: "office", Version: "local", HealthCheck: []string{"--version"}}}
	m := &Manager{Platform: "windows/arm64", LookupEnv: func(name string) string {
		if name == "OPENVIBELY_TOOL_TOOL" {
			return local
		}
		return ""
	}}
	r, err := m.Resolve(context.Background(), deps)
	if err != nil || r[0].Executable != local || r[0].Status != "local" || r[1].Status != "unsupported" {
		t.Fatalf("%v %v", r, err)
	}
	if err = os.WriteFile(local, []byte("#!/bin/sh\nexit 7\n"), 0755); err != nil {
		t.Fatal(err)
	}
	r, err = m.Resolve(context.Background(), deps)
	if err != nil || r[0].Status != "failed" || r[0].Executable != "" {
		t.Fatalf("bad local tool accepted: %v %v", r, err)
	}
}
func TestRejectUnsafeArchivesAndBadChecksums(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "C:/drive", "bin/../../escape", "bin\\escape"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			archive := filepath.Join(root, "package")
			os.WriteFile(archive, zipTool(t, name, "bad"), 0600)
			if err := prepare(context.Background(), Package{Format: "zip"}, archive, filepath.Join(root, "out")); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
	data := zipTool(t, "tool", "data")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(data) }))
	defer server.Close()
	m := &Manager{CacheDir: t.TempDir(), Platform: "linux/amd64", Client: server.Client()}
	r, err := m.Resolve(context.Background(), []Dependency{{Name: "tool", Version: "1", HealthCheck: []string{"version"}, Packages: map[string]Package{"linux/amd64": {URL: server.URL, SHA256: strings.Repeat("0", 64), Format: "zip", Executable: "tool"}}}})
	if err != nil || !strings.Contains(r[0].Error, "checksum mismatch") {
		t.Fatalf("%v %v", r, err)
	}
	filepath.Walk(m.CacheDir, func(p string, i os.FileInfo, e error) error {
		if e == nil && (i.Name() == ".ready" || strings.HasPrefix(i.Name(), ".prepare-")) {
			t.Errorf("partial install remains: %s", p)
		}
		return nil
	})
}
func TestCancelledInstallAndProgress(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	var stages []string
	m := &Manager{CacheDir: t.TempDir(), Platform: "linux/amd64", Client: server.Client(), Progress: func(p Progress) { stages = append(stages, p.Stage) }}
	dep := Dependency{Name: "tool", Version: "1", HealthCheck: []string{"version"}, Packages: map[string]Package{"linux/amd64": {URL: server.URL, SHA256: strings.Repeat("0", 64), Format: "raw", Executable: "tool"}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan []Result)
	go func() { r, _ := m.Resolve(ctx, []Dependency{dep}); done <- r }()
	<-started
	cancel()
	select {
	case r := <-done:
		if len(r) != 1 || r[0].Error == "" {
			t.Fatalf("%v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("download did not cancel")
	}
	if strings.Join(stages, ",") != "downloading,failed" {
		t.Fatalf("progress: %v", stages)
	}
}

func TestCancellationPreservesCachedExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	marker := filepath.Join(t.TempDir(), "check")
	data := zipTool(t, "bin/tool", fmt.Sprintf("#!/bin/sh\nif [ -f '%s' ]; then touch '%s.started'; while :; do :; done; fi\nexit 0\n", marker, marker))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(data) }))
	defer server.Close()
	dep := Dependency{Name: "tool", Version: "1", HealthCheck: []string{"version"}, Packages: map[string]Package{"linux/amd64": {URL: server.URL, SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), Format: "zip", Executable: "bin/tool"}}}
	manager := &Manager{CacheDir: t.TempDir(), Platform: "linux/amd64", Client: server.Client()}
	installed, err := manager.Resolve(context.Background(), []Dependency{dep})
	if err != nil || installed[0].Error != "" {
		t.Fatalf("install: %v %v", installed, err)
	}
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); manager.Resolve(ctx, []Dependency{dep}) }()
	for {
		if _, err := os.Stat(marker + ".started"); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("health check did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
	if _, err := os.Stat(installed[0].Executable); err != nil {
		t.Fatalf("cancellation removed working cache: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	server.Close()
	cached, err := manager.Resolve(context.Background(), []Dependency{dep})
	if err != nil || cached[0].Status != "cached" {
		t.Fatalf("offline cache after cancellation: %v %v", cached, err)
	}
}
