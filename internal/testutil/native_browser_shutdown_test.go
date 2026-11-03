package testutil

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/testutil/nativehost"
)

func TestNativeBrowserGracefulShutdown(t *testing.T) {
	if os.Getenv("OV_SHUTDOWN_PROBE") == "1" {
		defer nativehost.WatchShutdown(func() { os.Exit(0) })()
		time.Sleep(20 * time.Second)
		os.Exit(2)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestNativeBrowserGracefulShutdown$")
	cmd.Env = append(os.Environ(), "OV_SHUTDOWN_PROBE=1")
	ConfigureNativeBrowserStorage(t, cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if err := StopNativeBrowser(cmd, done); err != nil {
		t.Fatal(err)
	}
	if !cmd.ProcessState.Success() {
		t.Fatal("host did not exit normally")
	}
}
