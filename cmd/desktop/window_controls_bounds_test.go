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
