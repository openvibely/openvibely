package main

import (
	"math"
	"testing"
)

func TestCaptionBoundsScaling(t *testing.T) {
	b := captionBounds{X: 908, Y: 0, Width: 46, Height: 46, Viewport: 1000}
	for _, scale := range []float64{1, 1.25, 1.5, 2} {
		if !b.contains(930*scale, 20*scale, 1000*scale) {
			t.Fatal("missed caption at scale", scale)
		}
		if b.contains(960*scale, 20*scale, 1000*scale) || b.contains(930*scale, 46*scale, 1000*scale) {
			t.Fatal("captured outside caption", scale)
		}
	}
	b.Width = 0
	if b.contains(908, 10, 1000) {
		t.Fatal("inert caption must not capture input")
	}
	b.Viewport = math.NaN()
	if b.valid() {
		t.Fatal("accepted invalid viewport")
	}
}

func TestFitDesktopWindowSize(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		workW, workH, wantW, wantH int
	}{
		{"large display", 1920, 1040, 1280, 820},
		{"scaled VM", 1024, 720, 1024, 720},
		{"short display", 1366, 720, 1280, 720},
		{"unknown display", 0, 0, 1280, 820},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, h := fitDesktopWindowSize(1280, 820, tc.workW, tc.workH)
			if w != tc.wantW || h != tc.wantH {
				t.Fatalf("size = %dx%d, want %dx%d", w, h, tc.wantW, tc.wantH)
			}
		})
	}
}
