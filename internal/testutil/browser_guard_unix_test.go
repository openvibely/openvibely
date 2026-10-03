//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package testutil

import (
	"os/exec"
	"slices"
	"testing"
)

func TestGuardBrowserProcessAddsMockKeychainOnlyForChrome(t *testing.T) {
	for _, test := range []struct {
		name         string
		path         string
		wantMockFlag bool
	}{
		{name: "Chrome", path: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", wantMockFlag: true},
		{name: "Chromium", path: "/usr/bin/chromium-browser", wantMockFlag: true},
		{name: "helper binary", path: "/tmp/handler.test", wantMockFlag: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := &exec.Cmd{Path: test.path, Args: []string{test.path, "original-argument"}}
			GuardBrowserProcess(cmd)
			if got := slices.Contains(cmd.Args, "--use-mock-keychain"); got != test.wantMockFlag {
				t.Fatalf("mock-keychain flag present = %v, want %v; args = %q", got, test.wantMockFlag, cmd.Args)
			}
			if !slices.Contains(cmd.Args, "original-argument") {
				t.Fatalf("original argument lost: %q", cmd.Args)
			}
		})
	}
}
