package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/update"
)

const (
	fixtureHelperProcessEnv = "OPENVIBELY_FIXTURE_HELPER_PROCESS"
	fixtureParentProcessEnv = "OPENVIBELY_FIXTURE_PARENT_PROCESS"
	fixtureHelperArgsEnv    = "OPENVIBELY_FIXTURE_HELPER_ARGS"
)

func TestFixtureHelperBranchesApplyValidTimeout(t *testing.T) {
	for _, command := range []string{update.ExecutableUpdateHelperCommand, update.AppBundleUpdateHelperCommand} {
		t.Run(command, func(t *testing.T) {
			output, err := runFixtureHelperProcess(t, command, "", "1", true)
			if err == nil {
				t.Fatal("fixture helper unexpectedly exited successfully")
			}
			text := string(output)
			if !strings.Contains(text, "timed out waiting for parent process to exit") {
				t.Fatalf("fixture helper did not use the valid short wait timeout: %q", text)
			}
			if command == update.ExecutableUpdateHelperCommand && !strings.Contains(text, "[update-helper] started") {
				t.Fatalf("executable fixture helper did not reach update helper: %q", text)
			}
		})
	}
}

func TestFixtureHelperBranchesFatalBeforeRunOnInvalidTimeout(t *testing.T) {
	tests := []struct {
		name    string
		envName string
		value   string
		want    string
	}{
		{name: "wait timeout", envName: "OPENVIBELY_UPDATE_INTEGRATION_WAIT_TIMEOUT_MS", value: "not-a-duration", want: "parse update integration wait timeout"},
		{name: "validation timeout", envName: "OPENVIBELY_UPDATE_INTEGRATION_VALIDATION_TIMEOUT_MS", value: "not-a-duration", want: "parse update integration validation timeout"},
	}
	for _, test := range tests {
		for _, command := range []string{update.ExecutableUpdateHelperCommand, update.AppBundleUpdateHelperCommand} {
			t.Run(test.name+"/"+command, func(t *testing.T) {
				output, err := runFixtureHelperProcess(t, command, test.envName, test.value, false)
				if err == nil {
					t.Fatal("fixture helper unexpectedly exited successfully")
				}
				text := string(output)
				if !strings.Contains(text, test.want) {
					t.Fatalf("fixture fatal output = %q, want %q", text, test.want)
				}
				if strings.Contains(text, "[update-helper] started") {
					t.Fatalf("fixture helper started after invalid timeout: %q", text)
				}
				if strings.Contains(text, "timed out waiting for parent process to exit") {
					t.Fatalf("fixture helper ran after invalid timeout: %q", text)
				}
			})
		}
	}
}

func TestFixtureHelperProcess(t *testing.T) {
	if os.Getenv(fixtureHelperProcessEnv) != "1" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv(fixtureHelperArgsEnv)), &args); err != nil {
		t.Fatal(err)
	}
	os.Args = append([]string{"openvibely-testfixture"}, args...)
	main()
}

func TestFixtureParentProcess(t *testing.T) {
	if os.Getenv(fixtureParentProcessEnv) != "1" {
		return
	}
	select {}
}

func runFixtureHelperProcess(t *testing.T, command, invalidEnvName, timeoutValue string, expectHelperRun bool) ([]byte, error) {
	t.Helper()
	root := t.TempDir()
	current := filepath.Join(root, "current")
	if command == update.AppBundleUpdateHelperCommand {
		current = filepath.Join(root, "OpenVibely.app")
		if err := os.MkdirAll(current, 0o755); err != nil {
			t.Fatal(err)
		}
	} else if err := os.WriteFile(current, []byte("current"), 0o755); err != nil {
		t.Fatal(err)
	}

	metadata, err := json.Marshal(map[string]any{
		"arguments":           []string{"fixture"},
		"working_directory":   root,
		"executable_relative": "Contents/MacOS/OpenVibely",
	})
	if err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(root, "relaunch.json")
	if err := os.WriteFile(metadataPath, metadata, 0o644); err != nil {
		t.Fatal(err)
	}

	parentPID := 99999999
	var parent *exec.Cmd
	if expectHelperRun {
		parent = exec.Command(os.Args[0], "-test.run=TestFixtureParentProcess")
		parent.Env = fixtureTestEnv(fixtureParentProcessEnv + "=1")
		if err := parent.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = parent.Process.Kill()
			_, _ = parent.Process.Wait()
		})
		parentPID = parent.Process.Pid
	}

	args := []string{
		command,
		"--parent-pid", strconv.Itoa(parentPID),
		"--current", current,
		"--staged", current + ".openvibely-new",
		"--backup", current + ".openvibely-backup",
		"--health-url", "http://127.0.0.1:1/health",
		"--expected-version", "0.6.0",
		"--previous-version", "0.5.0",
		"--outcome-id", "fixture-timeout-test",
		"--recovery", "true",
		"--running-version", "0.6.0",
	}
	if command == update.ExecutableUpdateHelperCommand {
		args = append(args, "--relaunch-metadata", metadataPath)
	}
	if expectHelperRun {
		outcome := []byte(`{"id":"fixture-timeout-test","state":"authorized","previous_version":"0.5.0","desired_version":"0.6.0"}`)
		if err := os.WriteFile(current+".openvibely-outcome.json", outcome, 0o644); err != nil {
			t.Fatal(err)
		}
		claim := []byte(`{"id":"fixture-timeout-test","state":"recovering","previous_version":"0.5.0","desired_version":"0.6.0"}`)
		if err := os.WriteFile(current+".openvibely-recovery-claim.json", claim, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	argsJSON, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestFixtureHelperProcess")
	cmd.Env = fixtureTestEnv(
		fixtureHelperProcessEnv+"=1",
		fixtureHelperArgsEnv+"="+string(argsJSON),
		"OPENVIBELY_UPDATE_INTEGRATION_WAIT_TIMEOUT_MS=",
		"OPENVIBELY_UPDATE_INTEGRATION_VALIDATION_TIMEOUT_MS=",
		"OPENVIBELY_UPDATE_INTEGRATION_HELPER_LOG=1",
	)
	if invalidEnvName != "" {
		cmd.Env = fixtureTestEnvFrom(cmd.Env, invalidEnvName+"="+timeoutValue)
	} else {
		cmd.Env = fixtureTestEnvFrom(cmd.Env,
			"OPENVIBELY_UPDATE_INTEGRATION_WAIT_TIMEOUT_MS="+timeoutValue,
			"OPENVIBELY_UPDATE_INTEGRATION_VALIDATION_TIMEOUT_MS="+timeoutValue,
		)
	}
	if command == update.AppBundleUpdateHelperCommand {
		cmd.Stdin = bytes.NewReader(metadata)
	}
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("fixture helper subprocess exceeded test timeout: %v\n%s", ctx.Err(), output)
	}
	return output, err
}

func fixtureTestEnv(values ...string) []string {
	return fixtureTestEnvFrom(os.Environ(), values...)
}

func fixtureTestEnvFrom(base []string, values ...string) []string {
	env := append([]string(nil), base...)
	for _, value := range values {
		key, _, _ := strings.Cut(value, "=")
		filtered := env[:0]
		for _, existing := range env {
			if existingKey, _, _ := strings.Cut(existing, "="); existingKey != key {
				filtered = append(filtered, existing)
			}
		}
		env = append(filtered, value)
	}
	return env
}
