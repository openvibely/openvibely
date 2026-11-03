package handler

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/testutil/nativehost"
)

// Exercise the actual handler start/stop path, including the Unix shell guard.
func TestNativeHandlerBrowserGracefulShutdown(t *testing.T) {
	if os.Getenv("OV_HANDLER_SHUTDOWN_PROBE") == "1" {
		defer nativehost.WatchShutdown(func() { os.Exit(0) })()
		time.Sleep(20 * time.Second)
		os.Exit(2)
	}
	t.Setenv("OPENVIBELY_NATIVE_TEST_HOST", os.Args[0])
	cmd := exec.Command(os.Args[0], "-test.run=^TestNativeHandlerBrowserGracefulShutdown$")
	cmd.Env = append(os.Environ(), "OV_HANDLER_SHUTDOWN_PROBE=1")
	if err := startHandlerBrowserProcess(t, cmd); err != nil {
		t.Fatal(err)
	}
	stopHandlerBrowserProcess(t, cmd)
	if cmd.ProcessState == nil || !cmd.ProcessState.Success() {
		t.Fatal("native host did not acknowledge graceful shutdown")
	}
}
