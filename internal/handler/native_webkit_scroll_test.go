package handler

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// This opens a native window and delivers AppKit wheel input on a timer, while
// the web process inserts history. JS scrollTop tests miss WebKit's concurrent
// scrolling thread overwriting a layout correction with an earlier offset.
// Run explicitly on macOS: OPENVIBELY_TEST_WEBKIT=1 go test ./internal/handler
// -run '^TestTaskNativeWheelPagingInWebKit$' -count=1
func TestTaskNativeWheelPagingInWebKit(t *testing.T) {
	if testing.Short() || runtime.GOOS != "darwin" || os.Getenv("OPENVIBELY_TEST_WEBKIT") != "1" {
		t.Skip("requires opt-in native macOS window: OPENVIBELY_TEST_WEBKIT=1")
	}
	swift, err := exec.LookPath("swiftc")
	if err != nil {
		t.Fatal(err)
	}
	runner := filepath.Join(t.TempDir(), "webkit-wheel")
	if output, err := exec.Command(swift, "testdata/webkit_wheel.swift", "-o", runner).CombinedOutput(); err != nil {
		t.Fatalf("build native wheel runner: %v\n%s", err, output)
	}
	t.Setenv("CHROME_BIN", runner)
	testTaskPagedCachedNavigation(t, "native-wheel")
}
