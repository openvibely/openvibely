package main

import "math"

// Browser CSS coordinates are scaled against the actual HWND client width,
// accounting for both display scaling and webview zoom.
type captionBounds struct {
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Width    float64 `json:"width"`
	Height   float64 `json:"height"`
	Viewport float64 `json:"viewport"`
}

func (b captionBounds) valid() bool {
	for _, n := range []float64{b.X, b.Y, b.Width, b.Height, b.Viewport} {
		if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
			return false
		}
	}
	return b.Viewport > 0 && b.X+b.Width <= b.Viewport+1 && b.Height <= 100 && b.Y <= 100
}

func (b captionBounds) contains(x, y, clientWidth float64) bool {
	if !b.valid() || b.Width == 0 || clientWidth <= 0 {
		return false
	}
	scale := clientWidth / b.Viewport
	return x >= b.X*scale && x < (b.X+b.Width)*scale && y >= b.Y*scale && y < (b.Y+b.Height)*scale
}

// Work-area sizes are device-independent pixels, just like Wails window sizes.
func fitDesktopWindowSize(width, height, workWidth, workHeight int) (int, int) {
	if workWidth <= 0 || workHeight <= 0 {
		return width, height
	}
	return min(width, workWidth), min(height, workHeight)
}
