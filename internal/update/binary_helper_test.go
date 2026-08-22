package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBinaryHelperArgumentAndRelaunchParsingContracts(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "openvibely")
	staged := filepath.Join(root, "openvibely-new")
	backup := filepath.Join(root, "openvibely-backup")
	metadataPath := filepath.Join(root, "relaunch.json")
	cfg, err := ParseBinaryHelperArgs([]string{
		"--parent-pid", "1234",
		"--current", current,
		"--staged", staged,
		"--backup", backup,
		"--health-url", "http://127.0.0.1:4567/health",
		"--expected-version", "0.6.0",
		"--previous-version", "0.5.0",
		"--outcome-id", "outcome-1",
		"--running-version", "0.5.0",
		"--recovery", "true",
		"--relaunch-metadata", metadataPath,
	})
	if err != nil {
		t.Fatalf("ParseBinaryHelperArgs: %v", err)
	}
	if cfg.ParentPID != 1234 || !cfg.Recovery || cfg.RunningVersion != "0.5.0" || cfg.RelaunchMetadataPath != metadataPath {
		t.Fatalf("parsed cfg = %#v", cfg)
	}

	for _, args := range [][]string{
		{"--parent-pid"},
		{"parent-pid", "1"},
		{"--unsupported", "1"},
		{"--parent-pid", "1", "--parent-pid", "2"},
		{"--parent-pid", "bad", "--current", current, "--staged", staged, "--backup", backup, "--previous-version", "0.5.0", "--outcome-id", "outcome"},
		{"--parent-pid", "1", "--current", current, "--staged", staged, "--backup", backup, "--previous-version", "0.5.0", "--outcome-id", "outcome", "--recovery", "false"},
		{"--parent-pid", "1", "--current", current, "--staged", staged, "--backup", backup, "--previous-version", "0.5.0", "--outcome-id", "outcome", "--recovery", "true"},
		{"--parent-pid", "1", "--current", "relative", "--staged", staged, "--backup", backup, "--previous-version", "0.5.0", "--outcome-id", "outcome"},
		{"--parent-pid", "1", "--current", current, "--staged", staged, "--backup", backup, "--previous-version", "0.5.0", "--outcome-id", "outcome", "--relaunch-metadata", "relative.json"},
		{"--parent-pid", "1", "--current", current, "--staged", staged, "--backup", backup, "--previous-version", "0.5.0"},
	} {
		if _, err := ParseBinaryHelperArgs(args); err == nil {
			t.Fatalf("ParseBinaryHelperArgs(%v) unexpectedly succeeded", args)
		}
	}

	var relaunch BinaryHelperConfig
	metadataBytes, err := json.Marshal(binaryRelaunchMetadata{Arguments: []string{"openvibely", "serve"}, WorkingDirectory: root})
	if err != nil {
		t.Fatal(err)
	}
	metadata := string(metadataBytes)
	if err := LoadBinaryHelperRelaunch(strings.NewReader(metadata), &relaunch); err != nil {
		t.Fatalf("LoadBinaryHelperRelaunch: %v", err)
	}
	if len(relaunch.Arguments) != 2 || relaunch.Arguments[1] != "serve" || relaunch.WorkingDirectory != root {
		t.Fatalf("relaunch = %#v", relaunch)
	}

	if err := os.WriteFile(metadataPath, []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
	var fromFile BinaryHelperConfig
	if err := LoadBinaryHelperRelaunchFile(metadataPath, &fromFile); err != nil {
		t.Fatalf("LoadBinaryHelperRelaunchFile: %v", err)
	}
	if _, err := os.Stat(metadataPath); !os.IsNotExist(err) {
		t.Fatalf("metadata file was not removed after load: %v", err)
	}
	if len(fromFile.Arguments) != 2 || fromFile.WorkingDirectory != root {
		t.Fatalf("file relaunch = %#v", fromFile)
	}

	for _, input := range []string{
		`{"arguments":[],"working_directory":"/tmp"}`,
		`{"arguments":["openvibely"],"working_directory":"relative"}`,
		`{"arguments":["openvibely"],"working_directory":"/tmp","extra":true}`,
		`not-json`,
	} {
		if err := LoadBinaryHelperRelaunch(strings.NewReader(input), &BinaryHelperConfig{}); err == nil {
			t.Fatalf("LoadBinaryHelperRelaunch(%q) unexpectedly succeeded", input)
		}
	}
	if err := LoadBinaryHelperRelaunch(nil, &BinaryHelperConfig{}); err == nil {
		t.Fatal("nil relaunch reader unexpectedly succeeded")
	}
	if err := LoadBinaryHelperRelaunch(strings.NewReader(metadata), nil); err == nil {
		t.Fatal("nil relaunch config unexpectedly succeeded")
	}
	if err := LoadBinaryHelperRelaunchFile("", &BinaryHelperConfig{}); err == nil {
		t.Fatal("empty relaunch metadata path unexpectedly succeeded")
	}
	if err := LoadBinaryHelperRelaunchFile(filepath.Join(root, "missing.json"), &BinaryHelperConfig{}); err == nil {
		t.Fatal("missing relaunch metadata file unexpectedly succeeded")
	}
}
