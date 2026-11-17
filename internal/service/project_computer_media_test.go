package service

import (
	"context"
	"crypto/rand"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Uses an existing image only. No image build, pull, running desktop, or user
// browser profile is involved; fixtures are copied into a disposable container.
func TestProjectComputerDockerMediaCodecs(t *testing.T) {
	image := os.Getenv("OPENVIBELY_TEST_COMPUTER_IMAGE")
	if image == "" {
		t.Skip("set OPENVIBELY_TEST_COMPUTER_IMAGE for isolated Docker integration")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	name := "ov-codec-test-" + strings.ToLower(rand.Text())
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", args[0], err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// Register cleanup before creation so even an interrupted create is cleaned
	// up. Use a separate context because the test context may already be canceled.
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		out, err := exec.CommandContext(cleanup, "docker", "rm", "-f", name).CombinedOutput()
		if err != nil && !strings.Contains(string(out), "No such container") {
			t.Errorf("remove codec test container: %v\n%s", err, out)
		}
	})
	docker("create", "--pull=never", "--network=none", "--name", name,
		"--entrypoint", "node", image, "--test", "/tmp/computer-media/media_test.cjs")
	docker("cp", "testdata/computer-media", name+":/tmp/computer-media")
	t.Log(docker("start", "--attach", name))
	if code := docker("inspect", "--format={{.State.ExitCode}}", name); code != "0" {
		t.Fatalf("codec test exited with status %s", code)
	}
}
