package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvFileLoadsValuesWithoutOverridingExistingEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.env")
	if err := os.WriteFile(path, []byte("# comment\nOPENVIBELY_APP_DATA_DIR=/tmp/openvibely\nEXISTING=from-file\nQUOTED='hello world'\n"), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	t.Setenv("EXISTING", "from-env")
	unsetEnvForTest(t, "OPENVIBELY_APP_DATA_DIR")
	unsetEnvForTest(t, "QUOTED")

	if err := LoadEnvFile(path); err != nil {
		t.Fatalf("LoadEnvFile: %v", err)
	}
	if got := os.Getenv("OPENVIBELY_APP_DATA_DIR"); got != "/tmp/openvibely" {
		t.Fatalf("OPENVIBELY_APP_DATA_DIR=%q", got)
	}
	if got := os.Getenv("EXISTING"); got != "from-env" {
		t.Fatalf("EXISTING should not be overridden, got %q", got)
	}
	if got := os.Getenv("QUOTED"); got != "hello world" {
		t.Fatalf("QUOTED=%q", got)
	}
}

func TestLoadEnvFileRejectsMalformedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.env")
	if err := os.WriteFile(path, []byte("NOT_VALID\n"), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	if err := LoadEnvFile(path); err == nil {
		t.Fatal("expected malformed env file error")
	}
}

func TestLoadEnvFileRejectsEmptyKeyAndMissingFile(t *testing.T) {
	dir := t.TempDir()
	emptyKey := filepath.Join(dir, "empty.env")
	if err := os.WriteFile(emptyKey, []byte(" =value\n"), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	if err := LoadEnvFile(emptyKey); err == nil {
		t.Fatal("expected empty key error")
	}
	if err := LoadEnvFile(filepath.Join(dir, "missing.env")); err == nil {
		t.Fatal("expected missing file error")
	}
}

func TestDesktopConfigFilePathUsesOverrideOrDesktopDataDir(t *testing.T) {
	t.Setenv("OPENVIBELY_DESKTOP_CONFIG_FILE", "/tmp/openvibely/custom.env")
	if got := DesktopConfigFilePath(); got != "/tmp/openvibely/custom.env" {
		t.Fatalf("DesktopConfigFilePath override = %q", got)
	}

	t.Setenv("OPENVIBELY_DESKTOP_CONFIG_FILE", " ")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/openvibely-config")
	got := DesktopConfigFilePath()
	if filepath.Base(got) != "config.env" {
		t.Fatalf("DesktopConfigFilePath should end in config.env, got %q", got)
	}
}

func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
			return
		}
		_ = os.Unsetenv(key)
	})
}
