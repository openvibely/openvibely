//go:build openvibely_native_ui

package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeFileDropRequiresParentFixtureAndBoundedCoordinates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.txt")
	if err := os.WriteFile(path, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	valid := fileDrop{X: 100, Y: 110, Scale: 1}
	if err := validateFileDrop(valid, path); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "relative.txt", filepath.Dir(path), path + "-missing"} {
		if err := validateFileDrop(valid, path); err == nil {
			t.Errorf("accepted non-fixture %q", path)
		}
	}
	for _, e := range []fileDrop{
		{X: math.NaN(), Y: 110, Scale: 1}, {X: 100, Y: math.Inf(1), Scale: 1},
		{X: -1, Y: 110, Scale: 1}, {X: 100, Y: 5000, Scale: 1}, {X: 100, Y: 110, Scale: 0},
	} {
		if err := validateFileDrop(e, path); err == nil {
			t.Errorf("accepted invalid coordinates %+v", e)
		}
	}
}
