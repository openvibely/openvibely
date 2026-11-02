//go:build darwin || linux

package testutil

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
)

func TestChromeStorageConcurrentCleanup(t *testing.T) {
	chrome := ""
	for _, candidate := range []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "google-chrome", "chromium"} {
		if p, err := exec.LookPath(candidate); err == nil {
			chrome = p
			break
		}
	}
	if chrome == "" {
		t.Skip("Chrome unavailable")
	}
	var paths []string
	// The parent waits for parallel children before checking their storage cleanup.
	t.Run("parallel", func(t *testing.T) {
		for _, name := range []string{"first", "second", "third"} {
			t.Run(name+strings.Repeat("-long-test-name", 8), func(t *testing.T) {
				cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--disable-background-networking", "--dump-dom")
				ConfigureBrowserStorage(t, cmd)
				// Collect before Parallel; the slice is only accessed serially here.
				paths = append(paths, storageFlag(cmd, "--user-data-dir"), storageFlag(cmd, "--disk-cache-dir"), storageEnv(cmd, "TMPDIR"))
				t.Parallel()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Write([]byte("<html><body>isolated-browser-ok</body></html>"))
				}))
				defer server.Close()
				cmd.Args = append(cmd.Args, server.URL)
				output := filepath.Join(t.TempDir(), "chrome.log")
				f, err := os.Create(output)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				cmd.Stdout = f
				cmd.Stderr = f
				GuardBrowserProcess(cmd)
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				waited := make(chan struct{})
				go func() { cmd.Wait(); close(waited) }()
				defer StopBrowserProcessGroupWithWait(cmd, waited)
				deadline := time.Now().Add(20 * time.Second)
				for time.Now().Before(deadline) {
					data, _ := os.ReadFile(output)
					if strings.Contains(string(data), "isolated-browser-ok") {
						if runtime.GOOS == "darwin" {
							entries, err := os.ReadDir(storageEnv(cmd, "MAC_CHROMIUM_TMPDIR"))
							if err != nil || len(entries) == 0 {
								t.Fatalf("Chrome did not use its scratch directory: %v", err)
							}
						}
						return
					}
					time.Sleep(50 * time.Millisecond)
				}
				data, _ := os.ReadFile(output)
				t.Fatalf("Chrome did not render fixture: %s", data)
			})
		}
	})
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("browser storage remains: %s: %v", path, err)
		}
	}
}
