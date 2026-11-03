//go:build darwin && openvibely_native_ui

package pages

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/testutil"
)

// The same executable and origin must retain state across a page reload, but
// must not retain it when a fresh native window is launched in another process.
func TestNativeWebKitStorageIsolation(t *testing.T) {
	if os.Getenv("OPENVIBELY_RUN_NATIVE_UI") != "1" {
		t.Skip("requires OPENVIBELY_RUN_NATIVE_UI=1")
	}
	results := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/result" {
			results <- r.URL.Query().Get("status")
			w.WriteHeader(204)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<script>
(async()=>{
 try {
  if (!location.search) {
   if (localStorage.getItem('native-storage-probe') || document.cookie.includes('native-storage-probe=')) throw Error('state survived process restart');
   localStorage.setItem('native-storage-probe','present');
   document.cookie='native-storage-probe=present; Max-Age=3600; Path=/';
   location.replace('/?reload=1');
  } else {
   if (localStorage.getItem('native-storage-probe') !== 'present' || !document.cookie.includes('native-storage-probe=present')) throw Error('state missing on reload');
   await fetch('/result?status=pass');
  }
 } catch(e) { await fetch('/result?status='+encodeURIComponent(String(e))); }
})();
</script>`))
	}))
	defer server.Close()
	var homes []string
	for i := 0; i < 2; i++ {
		t.Run(string(rune('A'+i)), func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^$")
			cmd.Env = append(os.Environ(), "OPENVIBELY_NATIVE_UI_HELPER=1", "OPENVIBELY_NATIVE_UI_URL="+server.URL+"/")
			var output bytes.Buffer
			testutil.ConfigureNativeBrowserStorage(t, cmd)
			var home string
			for _, entry := range cmd.Env {
				if strings.HasPrefix(entry, "CFFIXED_USER_HOME=") {
					home = strings.TrimPrefix(entry, "CFFIXED_USER_HOME=")
				}
			}
			homes = append(homes, home)
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			stopped := false
			defer func() {
				if !stopped {
					if err := testutil.StopNativeBrowser(cmd, done); err != nil {
						t.Errorf("stop native host: %v", err)
					}
				}
			}()
			select {
			case result := <-results:
				// Require actual persistent localStorage inside the redirected home.
				// This prevents a pass merely because the host was killed before flushing.
				found := false
				deadline := time.Now().Add(10 * time.Second)
				for !found && time.Now().Before(deadline) {
					_ = filepath.WalkDir(filepath.Join(home, "Library", "WebKit"), func(path string, entry os.DirEntry, err error) error {
						if err == nil && entry.Name() == "localstorage.sqlite3" {
							found = true
						}
						return nil
					})
					if !found {
						time.Sleep(50 * time.Millisecond)
					}
				}
				if !found {
					t.Fatal("localStorage database missing from temporary home")
				}
				if err := testutil.StopNativeBrowser(cmd, done); err != nil {
					t.Errorf("stop native host: %v", err)
				}
				stopped = true
				if result != "pass" {
					t.Fatalf("%s\n%s", result, output.String())
				}
			case err := <-done:
				stopped = true
				t.Fatalf("native helper exited: %v\n%s", err, output.String())
			case <-time.After(30 * time.Second):
				t.Fatal("native storage probe timed out")
			}
		})
	}
	for _, home := range homes {
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Errorf("native storage remains: %s", home)
		}
	}
}
