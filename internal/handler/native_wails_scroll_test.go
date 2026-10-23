package handler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Reuses the real HTTP/SQLite fixture and assertions with each OS's Wails webview.
func TestTaskNativeWheelPagingInWails(t *testing.T) {
	if testing.Short() || os.Getenv("OPENVIBELY_RUN_NATIVE_UI") != "1" {
		t.Skip("requires OPENVIBELY_RUN_NATIVE_UI=1 and a graphical session")
	}
	tags := "openvibely_native_ui"
	if runtime.GOOS == "linux" {
		tags += ",gtk3"
	}
	name := "native-chat"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	runner := filepath.Join(t.TempDir(), name)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-tags", tags, "-o", runner, "../testutil/nativechat")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build Wails test host: %v\n%s", err, output)
	}
	t.Setenv("CHROME_BIN", runner)
	t.Run("ordinary-page-scroll-range", TestOrdinaryPagesKeepViewportScrollAndBottomInsetInChrome)
	// Run sequentially: each scenario owns a native window and input stream.
	for _, scenario := range []struct{ name, mode string }{
		{"during-wheel", "native-wheel"},
		{"after-wheel", "native-wheel-delayed"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			testTaskPagedCachedNavigation(t, scenario.mode)
		})
	}
}
