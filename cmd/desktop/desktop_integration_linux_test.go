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
	t.Setenv("HOME", root)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_BIN_HOME", binHome)
	t.Setenv("PATH", "")

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
	if !strings.Contains(string(desktopEntry), `Exec="`+escapeDesktopExec(executable)+`"`) {
		t.Fatalf("desktop entry does not reference installed executable:\n%s", desktopEntry)
	}
	for _, size := range []string{"16", "24", "32", "48", "64", "128", "256", "512", "1024"} {
		icon := filepath.Join(dataHome, "icons", "hicolor", size+"x"+size, "apps", "com.openvibely.desktop.png")
		if info, err := os.Stat(icon); err != nil || info.Size() == 0 {
			t.Fatalf("installed %spx icon = %v, err = %v", size, info, err)
		}
	}
}

func TestEscapeDesktopExec(t *testing.T) {
	if got, want := escapeDesktopExec(`/tmp/a b/\100%/"$app`), `/tmp/a b/\\\\100%%/\"\\$app`; got != want {
		t.Fatalf("escapeDesktopExec() = %q, want %q", got, want)
	}
}
