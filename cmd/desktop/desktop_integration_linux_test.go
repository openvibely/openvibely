//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleDesktopIntegrationCommandIgnoresNormalLaunch(t *testing.T) {
	handled, err := handleDesktopIntegrationCommand([]string{"openvibely-desktop"})
	if err != nil || handled {
		t.Fatalf("handled = %v, err = %v; want false, nil", handled, err)
	}
}

func TestInstallLinuxDesktopIntegration(t *testing.T) {
	root := t.TempDir()
	dataHome := filepath.Join(root, "data")
	binHome := filepath.Join(root, "bin")
	mockBin := filepath.Join(root, "mock-bin")
	cacheArgsPath := filepath.Join(root, "icon-cache-args")
	if err := os.MkdirAll(mockBin, 0o755); err != nil {
		t.Fatal(err)
	}
	cacheCommand := filepath.Join(mockBin, "gtk-update-icon-cache")
	if err := os.WriteFile(cacheCommand, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$OPENVIBELY_ICON_CACHE_ARGS\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_BIN_HOME", binHome)
	t.Setenv("OPENVIBELY_ICON_CACHE_ARGS", cacheArgsPath)
	t.Setenv("PATH", mockBin)

	if err := installLinuxDesktopIntegration(); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(binHome, "openvibely-desktop")
	if info, err := os.Stat(executable); err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("installed executable mode = %v, err = %v", info, err)
	}
	desktopEntry, err := os.ReadFile(filepath.Join(dataHome, "applications", "com.openvibely.desktop.desktop"))
	if err != nil {
		t.Fatal(err)
	}
	escapedExecutable, err := escapeDesktopExec(executable)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(desktopEntry), `Exec="`+escapedExecutable+`"`) {
		t.Fatalf("desktop entry does not reference installed executable:\n%s", desktopEntry)
	}
	for _, size := range []string{"16", "24", "32", "48", "64", "128", "256", "512", "1024"} {
		icon := filepath.Join(dataHome, "icons", "hicolor", size+"x"+size, "apps", "com.openvibely.desktop.png")
		if info, err := os.Stat(icon); err != nil || info.Size() == 0 {
			t.Fatalf("installed %spx icon = %v, err = %v", size, info, err)
		}
	}
	cacheArgs, err := os.ReadFile(cacheArgsPath)
	if err != nil {
		t.Fatal(err)
	}
	wantCacheArgs := "-q\n-t\n-f\n" + filepath.Join(dataHome, "icons", "hicolor") + "\n"
	if string(cacheArgs) != wantCacheArgs {
		t.Fatalf("gtk-update-icon-cache args = %q, want %q", cacheArgs, wantCacheArgs)
	}
}

func TestInstallLinuxDesktopIntegrationRefreshesDesktopDatabaseAfterIconCacheFailure(t *testing.T) {
	root := t.TempDir()
	dataHome := filepath.Join(root, "data")
	mockBin := filepath.Join(root, "mock-bin")
	databaseArgsPath := filepath.Join(root, "desktop-database-args")
	if err := os.MkdirAll(mockBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mockBin, "gtk-update-icon-cache"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	databaseCommand := filepath.Join(mockBin, "update-desktop-database")
	if err := os.WriteFile(databaseCommand, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$OPENVIBELY_DESKTOP_DATABASE_ARGS\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_BIN_HOME", filepath.Join(root, "bin"))
	t.Setenv("OPENVIBELY_DESKTOP_DATABASE_ARGS", databaseArgsPath)
	t.Setenv("PATH", mockBin)

	err := installLinuxDesktopIntegration()
	if err == nil || !strings.Contains(err.Error(), "refresh application icon cache") {
		t.Fatalf("installLinuxDesktopIntegration() error = %v, want icon-cache refresh error", err)
	}
	databaseArgs, readErr := os.ReadFile(databaseArgsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	wantDatabaseArgs := filepath.Join(dataHome, "applications") + "\n"
	if string(databaseArgs) != wantDatabaseArgs {
		t.Fatalf("update-desktop-database args = %q, want %q", databaseArgs, wantDatabaseArgs)
	}
}

func TestEscapeDesktopExec(t *testing.T) {
	got, err := escapeDesktopExec(`/tmp/a b/\100%/"$app`)
	if err != nil {
		t.Fatal(err)
	}
	if want := `/tmp/a b/\\\\100%%/\"\\$app`; got != want {
		t.Fatalf("escapeDesktopExec() = %q, want %q", got, want)
	}
}

func TestEscapeDesktopExecRejectsUnsupportedCharacters(t *testing.T) {
	for _, path := range []string{"/tmp/line\nbreak", "/tmp/carriage\rreturn", "/tmp/tab\tname", "/tmp/name=value"} {
		if _, err := escapeDesktopExec(path); err == nil {
			t.Fatalf("escapeDesktopExec(%q) succeeded, want error", path)
		}
	}
}

func TestInstallLinuxDesktopIntegrationRejectsPathBeforeWriting(t *testing.T) {
	root := t.TempDir()
	dataHome := filepath.Join(root, "data")
	binHome := filepath.Join(root, "invalid=bin")
	t.Setenv("HOME", root)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_BIN_HOME", binHome)
	t.Setenv("PATH", "")

	if err := installLinuxDesktopIntegration(); err == nil {
		t.Fatal("installLinuxDesktopIntegration() succeeded, want invalid-path error")
	}
	for _, path := range []string{dataHome, binHome} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("installation wrote %s before rejecting the path: %v", path, err)
		}
	}
}
