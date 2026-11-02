package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func storageFlag(cmd *exec.Cmd, flag string) string {
	for _, arg := range cmd.Args {
		if strings.HasPrefix(arg, flag+"=") {
			return strings.TrimPrefix(arg, flag+"=")
		}
	}
	return ""
}
func storageEnv(cmd *exec.Cmd, key string) string {
	for _, entry := range cmd.Env {
		k, v, _ := strings.Cut(entry, "=")
		if k == key {
			return v
		}
	}
	return ""
}

func TestBrowserStorageIsolationAndCleanup(t *testing.T) {
	parentTemp := os.Getenv("TMPDIR")
	var paths []string
	t.Run("launches", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			cmd := &exec.Cmd{Path: "/fake/chrome", Args: []string{"chrome", "--user-data-dir", "/shared/profile", "--disk-cache-dir=/shared/cache", "--headless=new", "http://localhost/"}, Env: []string{"KEEP=value", "TMPDIR=/shared/tmp", "TMP=old", "TEMP=old", "MAC_CHROMIUM_TMPDIR=old"}}
			ConfigureBrowserStorage(t, cmd)
			profile, cache, scratch := storageFlag(cmd, "--user-data-dir"), storageFlag(cmd, "--disk-cache-dir"), storageEnv(cmd, "TMPDIR")
			for _, path := range []string{profile, cache, scratch} {
				if path == "" {
					t.Fatal("missing browser storage")
				}
				for _, prev := range paths {
					if path == prev {
						t.Fatal("launch directories overlap")
					}
				}
				paths = append(paths, path)
				if err := os.WriteFile(filepath.Join(path, "probe"), []byte("data"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if storageEnv(cmd, "KEEP") != "value" || storageEnv(cmd, "TMP") != scratch || storageEnv(cmd, "TEMP") != scratch || storageEnv(cmd, "MAC_CHROMIUM_TMPDIR") != scratch {
				t.Fatalf("child environment: %v", cmd.Env)
			}
			if strings.Contains(strings.Join(cmd.Args, " "), "/shared/") {
				t.Fatalf("old storage flags survived: %v", cmd.Args)
			}
			if cmd.Args[len(cmd.Args)-1] != "http://localhost/" {
				t.Fatal("URL changed")
			}
		}
	})
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("storage remains after cleanup: %s: %v", path, err)
		}
	}
	if os.Getenv("TMPDIR") != parentTemp {
		t.Fatal("changed parent temp environment")
	}
}

func TestBrowserStorageLeavesNativeHelpersUnchanged(t *testing.T) {
	cmd := &exec.Cmd{Path: "/fake/native-visible", Args: []string{"native-visible", "--headless=new", "http://localhost/"}, Env: []string{"TMPDIR=original"}}
	args, env := append([]string(nil), cmd.Args...), append([]string(nil), cmd.Env...)
	ConfigureBrowserStorage(t, cmd)
	if !reflect.DeepEqual(cmd.Args, args) || !reflect.DeepEqual(cmd.Env, env) {
		t.Fatal("native helper configuration changed")
	}
}
