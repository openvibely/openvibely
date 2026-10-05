package main

import (
	"errors"
	"testing"
)

func TestDesktopFolderSelection(t *testing.T) {
	canceled := errors.New("cancelled by user")
	failed := errors.New("unable to open folder dialog")
	for _, tc := range []struct {
		name, goos, path string
		err              error
		wantPath         string
		wantCanceled     bool
		wantErr          error
	}{
		{name: "Windows cancel", goos: "windows", err: canceled, wantCanceled: true},
		{name: "Windows selection", goos: "windows", path: `C:\repo`, wantPath: `C:\repo`},
		{name: "macOS cancel", goos: "darwin", wantCanceled: true},
		{name: "Linux cancel", goos: "linux", wantCanceled: true},
		{name: "macOS selection", goos: "darwin", path: "/tmp/repo", wantPath: "/tmp/repo"},
		{name: "Linux selection", goos: "linux", path: "/tmp/repo", wantPath: "/tmp/repo"},
		{name: "Windows failure", goos: "windows", err: failed, wantErr: failed},
		{name: "Linux failure", goos: "linux", err: failed, wantErr: failed},
		{name: "macOS failure", goos: "darwin", err: failed, wantErr: failed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, canceled, err := desktopFolderSelection(tc.goos, tc.path, tc.err)
			if path != tc.wantPath || canceled != tc.wantCanceled || err != tc.wantErr {
				t.Fatalf("got (%q, %v, %v); want (%q, %v, %v)", path, canceled, err, tc.wantPath, tc.wantCanceled, tc.wantErr)
			}
		})
	}
}
