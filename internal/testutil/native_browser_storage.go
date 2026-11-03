package testutil

import (
	"github.com/openvibely/openvibely/internal/testutil/nativehost"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ConfigureNativeBrowserStorage isolates a native test host without changing
// Wails or the parent's environment. Call after setting cmd.Env and before
// starting the process. Stop the host before t.TempDir cleanup runs.
func ConfigureNativeBrowserStorage(t testing.TB, cmd *exec.Cmd) {
	t.Helper()
	if cmd == nil || cmd.Process != nil {
		t.Fatal("native storage requires an unstarted command")
	}
	configureNativeBrowserStorage(t, cmd, runtime.GOOS)
}

func configureNativeBrowserStorage(t testing.TB, cmd *exec.Cmd, platform string) {
	t.Helper()
	root := t.TempDir()
	// WebKit/WebView2 use out-of-process services which can finish writing after
	// the host exits. Retry deletion errors before TempDir's final cleanup;
	// this is not a signal that all OS-managed browser services have exited.
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for {
			err := os.RemoveAll(root)
			if err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("native browser storage still busy after host shutdown: %s: %v", root, err)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	var dirs map[string]string
	switch platform {
	case "darwin":
		dirs = map[string]string{"CFFIXED_USER_HOME": filepath.Join(root, "home")}
	case "linux":
		dirs = map[string]string{"XDG_DATA_HOME": filepath.Join(root, "data"), "XDG_CACHE_HOME": filepath.Join(root, "cache")}
	case "windows":
		dirs = map[string]string{"APPDATA": filepath.Join(root, "appdata")}
	default:
		t.Fatalf("unsupported native browser platform: %s", platform)
	}
	env := cmd.Environ()
	filtered := make([]string, 0, len(env)+len(dirs))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if platform == "windows" {
			key = strings.ToUpper(key)
		}
		if key == nativehost.ShutdownFileEnv {
			continue
		}
		if _, replaced := dirs[key]; !replaced {
			filtered = append(filtered, entry)
		}
	}
	filtered = append(filtered, nativehost.ShutdownFileEnv+"="+filepath.Join(root, "shutdown"))
	cmd.WaitDelay = 5 * time.Second
	for key, dir := range dirs {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		filtered = append(filtered, key+"="+dir)
	}
	cmd.Env = filtered
}
