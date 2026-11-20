//go:build !darwin

package attachment

import "image"

func resizeRaster(src image.Image, w, h int) *image.NRGBA {
	return resizeLanczos(src, w, h)
}

func nativeGIFFrame(_ []byte, _, _ int) image.Image { return nil }
