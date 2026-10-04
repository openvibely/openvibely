package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Runs the actual production GTK overlay in a separate process. A real window
// manager is required for maximize/restore, not just an X server.
func TestNativeLinuxWindowControls(t *testing.T) {
	if os.Getenv("OPENVIBELY_RUN_NATIVE_UI") != "1" {
		t.Skip("set OPENVIBELY_RUN_NATIVE_UI=1 with a display and window manager")
	}
	for _, toolkit := range []string{"gtk+-3.0", "gtk4"} {
		t.Run(toolkit, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			flags, err := exec.CommandContext(ctx, "pkg-config", "--cflags", "--libs", toolkit).CombinedOutput()
			if err != nil {
				t.Fatalf("GTK development packages: %v: %s", err, flags)
			}
			binary := filepath.Join(t.TempDir(), "caption-check")
			args := append([]string{"testdata/linux_caption_check.c", "-o", binary}, strings.Fields(string(flags))...)
			if output, err := exec.CommandContext(ctx, "cc", args...).CombinedOutput(); err != nil {
				t.Fatalf("compile native controls: %v: %s", err, output)
			}
			if output, err := exec.CommandContext(ctx, binary).CombinedOutput(); err != nil {
				t.Fatalf("native controls: %v: %s", err, output)
			}
		})
	}
}
