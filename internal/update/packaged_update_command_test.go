package update

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type unreadHelperInput struct{ t *testing.T }

func (r unreadHelperInput) Read([]byte) (int, error) {
	r.t.Fatal("dispatcher unexpectedly read stdin")
	return 0, io.EOF
}

func TestPackagedUpdateCommandUnsupported(t *testing.T) {
	for _, command := range []string{"", "serve", "healthcheck", AppBundleUpdateHelperCommand} {
		handled, err := RunPackagedUpdateHelperCommand(context.Background(), command, []string{"--invalid"}, unreadHelperInput{t}, PackagedUpdateHelperOptions{})
		if handled || err != nil {
			t.Fatalf("command %q: handled=%t err=%v", command, handled, err)
		}
	}
}

func TestPackagedUpdateCommandDispatch(t *testing.T) {
	for _, transport := range []string{"executable stdin", "executable file", "app-bundle stdin"} {
		for _, failure := range []string{"", "arguments", "metadata", "wait timeout", "validation timeout", "missing file"} {
			if failure == "missing file" && transport != "executable file" {
				continue
			}
			t.Run(transport+"/"+failure, func(t *testing.T) {
				t.Setenv("OPENVIBELY_UPDATE_INTEGRATION_WAIT_TIMEOUT_MS", "100")
				t.Setenv("OPENVIBELY_UPDATE_INTEGRATION_VALIDATION_TIMEOUT_MS", "200")
				root := t.TempDir()
				staged := LocalStagedUpdate{
					InstallPath: filepath.Join(root, "current"), Version: "0.6.0",
					PreviousVersion: "0.5.0", OutcomeID: "dispatch-test",
				}
				staged.ArtifactPath = staged.InstallPath + ".openvibely-new"
				staged.BackupPath = staged.InstallPath + ".openvibely-backup"
				if err := writePackagedUpdateHelperOutcome(staged, packagedUpdateOutcomePrepared); err != nil {
					t.Fatal(err)
				}
				args := []string{
					"--parent-pid", "99999999", "--current", staged.InstallPath,
					"--staged", staged.ArtifactPath, "--backup", staged.BackupPath,
					"--expected-version", staged.Version, "--previous-version", staged.PreviousVersion,
					"--outcome-id", staged.OutcomeID, "--health-url", "http://127.0.0.1:1/health",
				}
				metadata, err := json.Marshal(packagedUpdateRelaunchMetadata{
					Arguments: []string{"fixture", "serve"}, WorkingDirectory: root,
					ExecutableRelative: "Contents/MacOS/OpenVibely",
				})
				if err != nil {
					t.Fatal(err)
				}
				want := ""
				switch failure {
				case "arguments":
					args = append(args, "--invalid")
					want = "argument"
				case "metadata":
					metadata = []byte(`{"arguments":[]}`)
					want = "relaunch metadata"
				case "wait timeout":
					t.Setenv("OPENVIBELY_UPDATE_INTEGRATION_WAIT_TIMEOUT_MS", "invalid")
					want = "parse update integration wait timeout"
				case "validation timeout":
					t.Setenv("OPENVIBELY_UPDATE_INTEGRATION_VALIDATION_TIMEOUT_MS", "invalid")
					want = "parse update integration validation timeout"
				case "missing file":
					want = "open executable update helper relaunch metadata"
				}
				command := ExecutableUpdateHelperCommand
				var stdin io.Reader = strings.NewReader(string(metadata))
				if transport == "app-bundle stdin" {
					command = AppBundleUpdateHelperCommand
				}
				metadataPath := filepath.Join(root, "metadata.json")
				if transport == "executable file" {
					if failure != "missing file" {
						if err := os.WriteFile(metadataPath, metadata, 0o600); err != nil {
							t.Fatal(err)
						}
					}
					args = append(args, "--relaunch-metadata", metadataPath)
					stdin = unreadHelperInput{t}
				} else if failure == "arguments" {
					stdin = unreadHelperInput{t}
				}
				// A cancelled context stops the real helper after it claims the
				// prepared handoff, before replacement or relaunch can happen.
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				handled, err := RunPackagedUpdateHelperCommand(ctx, command, args, stdin, PackagedUpdateHelperOptions{AppBundle: true})
				if !handled {
					t.Fatal("helper command was not handled")
				}
				if failure == "" {
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("helper error = %v, want context cancellation", err)
					}
					outcome, err := readPackagedUpdateHelperOutcome(staged)
					if err != nil || outcome.State != packagedUpdateOutcomeCancelled {
						t.Fatalf("helper did not claim and cancel handoff: %+v, %v", outcome, err)
					}
				} else {
					if err == nil || !strings.Contains(err.Error(), want) {
						t.Fatalf("error = %v, want %q", err, want)
					}
					if _, err := os.Stat(packagedUpdateHelperOutcomePath(staged.InstallPath)); !os.IsNotExist(err) {
						t.Fatalf("invalid input reached helper: outcome stat = %v", err)
					}
					if _, err := readPackagedUpdateHelperPrepared(staged); err != nil {
						t.Fatalf("invalid input changed prepared handoff: %v", err)
					}
				}
				if transport == "executable file" && failure != "arguments" {
					if _, err := os.Stat(metadataPath); !os.IsNotExist(err) {
						t.Fatalf("metadata file was not consumed: %v", err)
					}
				}
			})
		}
	}
}
