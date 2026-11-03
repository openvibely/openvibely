package testutil

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestNativeBrowserStorageIsolation(t *testing.T) {
	for platform, keys := range map[string][]string{
		"darwin": {"CFFIXED_USER_HOME"}, "linux": {"XDG_DATA_HOME", "XDG_CACHE_HOME"}, "windows": {"APPDATA"},
	} {
		t.Run(platform, func(t *testing.T) {
			parent := os.Environ()
			paths := map[string]bool{}
			t.Run("hosts", func(t *testing.T) {
				for i := 0; i < 2; i++ {
					cmd := exec.Command("unused-native-host")
					cmd.Env = []string{"KEEP=value"}
					for _, key := range keys {
						cmd.Env = append(cmd.Env, key+"=/old")
					}
					if platform == "windows" {
						cmd.Env = append(cmd.Env, "appdata=/old-case")
					}
					configureNativeBrowserStorage(t, cmd, platform)
					if storageEnv(cmd, "KEEP") != "value" {
						t.Fatal("lost child environment")
					}
					for _, key := range keys {
						count := 0
						for _, entry := range cmd.Env {
							k, _, _ := strings.Cut(entry, "=")
							if strings.EqualFold(k, key) {
								count++
							}
						}
						if count != 1 {
							t.Fatalf("duplicate override for %s", key)
						}
						path := storageEnv(cmd, key)
						if path == "" || path == "/old" || paths[path] {
							t.Fatalf("storage not isolated: %q", path)
						}
						paths[path] = true
						if info, err := os.Stat(path); err != nil || !info.IsDir() {
							t.Fatalf("missing directory %s", path)
						}
					}
				}
			})
			for path := range paths {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("storage not cleaned: %s", path)
				}
			}
			if strings.Join(parent, "\n") != strings.Join(os.Environ(), "\n") {
				t.Fatal("parent environment changed")
			}
		})
	}
}
