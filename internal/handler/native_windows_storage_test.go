package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/testutil"
)

// Verify the installed Wails loader actually uses the redirected APPDATA,
// rather than merely checking that the parent constructed an environment.
func TestNativeWindowsStorageIsolation(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("OPENVIBELY_RUN_NATIVE_UI") != "1" {
		t.Skip("requires opted-in native Windows session")
	}
	runner := buildNativeChatHost(t)
	results := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/native-test-bridge":
			w.WriteHeader(204)
		case "/result":
			results <- r.URL.Query().Get("status")
			w.WriteHeader(204)
		default:
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<script>try{if(localStorage.getItem('probe'))throw Error('shared profile');localStorage.setItem('probe','written');fetch('/result?status=pass')}catch(e){fetch('/result?status='+encodeURIComponent(String(e)))}</script>`))
		}
	}))
	defer server.Close()
	var roots []string
	for _, name := range []string{"first", "second"} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(runner, server.URL)
			testutil.ConfigureNativeBrowserStorage(t, cmd)
			root := ""
			for _, entry := range cmd.Env {
				key, value, _ := strings.Cut(entry, "=")
				if strings.EqualFold(key, "APPDATA") {
					root = value
				}
			}
			roots = append(roots, root)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			exited := false
			defer func() {
				if !exited {
					if err := testutil.StopNativeBrowser(cmd, done); err != nil {
						t.Errorf("stop native host: %v", err)
					}
				}
			}()
			select {
			case result := <-results:
				if result != "pass" {
					t.Fatal(result)
				}
			case err := <-done:
				exited = true
				t.Fatalf("host exited early: %v", err)
			case <-time.After(30 * time.Second):
				t.Fatal("storage probe timed out")
			}
			profile := filepath.Join(root, filepath.Base(runner))
			found := false
			deadline := time.Now().Add(10 * time.Second)
			for !found && time.Now().Before(deadline) {
				_ = filepath.WalkDir(profile, func(path string, entry os.DirEntry, err error) error {
					if err == nil && entry.IsDir() && entry.Name() == "Local Storage" {
						found = true
					}
					return nil
				})
				if !found {
					time.Sleep(50 * time.Millisecond)
				}
			}
			if !found {
				t.Fatalf("WebView2 did not create local storage under %s", profile)
			}
		})
	}
	for _, root := range roots {
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Errorf("profile remains: %s", root)
		}
	}
}
