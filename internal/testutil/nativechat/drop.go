//go:build openvibely_native_ui

package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// The file is supplied by the parent test, never by page JavaScript.
type fileDrop struct{ X, Y, Scale float64 }

func validateFileDrop(e fileDrop, path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("native drop fixture file missing")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("native drop fixture must be a regular file: %v", err)
	}
	for _, value := range []float64{e.X, e.Y, e.Scale} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("invalid drop coordinates")
		}
	}
	if e.X < 0 || e.Y < 0 || e.X > 4096 || e.Y > 4096 || e.Scale <= 0 || e.Scale > 8 {
		return fmt.Errorf("invalid drop coordinates")
	}
	return nil
}
