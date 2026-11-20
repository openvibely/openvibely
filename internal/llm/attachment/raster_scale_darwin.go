package attachment

import (
	"image"
	"image/draw"
	"sync"

	"github.com/ebitengine/purego"
)

// vImage_Buffer has the same layout on both supported macOS architectures.
type pixelBuffer struct {
	data                  *byte
	height, width, stride uintptr
}

type scaleBuffer func(src, dst *pixelBuffer, temp uintptr, flags uint32) int64

var rasterScalers = sync.OnceValues(func() (rgba, gray scaleBuffer) {
	lib, err := purego.Dlopen("/System/Library/Frameworks/Accelerate.framework/Accelerate", purego.RTLD_LAZY|purego.RTLD_LOCAL)
	if err != nil {
		return nil, nil
	}
	purego.RegisterLibFunc(&rgba, lib, "vImageScale_ARGB8888")
	purego.RegisterLibFunc(&gray, lib, "vImageScale_Planar8")
	return rgba, gray
})

func resizeRaster(src image.Image, w, h int) *image.NRGBA {
	rgba, gray := rasterScalers()
	if rgba == nil {
		return resizeLanczos(src, w, h)
	}
	b := src.Bounds()
	var pixels []byte
	var stride int
	channels := 4
	scale := rgba
	switch img := src.(type) {
	case *image.NRGBA:
		pixels, stride = img.Pix, img.Stride
	case *image.RGBA:
		if !img.Opaque() {
			return resizeLanczos(src, w, h)
		}
		pixels, stride = img.Pix, img.Stride
	case *image.YCbCr, *image.NYCbCrA, *image.CMYK:
		// The native scaler requires packed pixels. Keep the streaming path
		// for large images to bound the extra allocation for conversion.
		if b.Dx() > maxPackedImageBytes/4/b.Dy() {
			return resizeLanczos(src, w, h)
		}
		packed := image.NewNRGBA(b)
		draw.Draw(packed, b, src, b.Min, draw.Src)
		pixels, stride = packed.Pix, packed.Stride
	case *image.Gray:
		pixels, stride, scale, channels = img.Pix, img.Stride, gray, 1
	case *image.Gray16:
		// Scale the high byte of each sample, as in the eight-bit decode path.
		stride, scale, channels = b.Dx(), gray, 1
		pixels = make([]byte, stride*b.Dy())
		for y := range b.Dy() {
			row := img.Pix[y*img.Stride:]
			for x := range stride {
				pixels[y*stride+x] = row[2*x]
			}
		}
	default:
		return resizeLanczos(src, w, h)
	}
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	// A single-channel result fits in the same allocation. Expand it backwards below.
	input := pixelBuffer{&pixels[0], uintptr(b.Dy()), uintptr(b.Dx()), uintptr(stride)}
	output := pixelBuffer{&out.Pix[0], uintptr(h), uintptr(w), uintptr(w * channels)}
	if scale(&input, &output, 0, 0) != 0 {
		return resizeLanczos(src, w, h)
	}
	if channels == 1 {
		for i := w*h - 1; i >= 0; i-- {
			g := out.Pix[i]
			j := 4 * i
			out.Pix[j], out.Pix[j+1], out.Pix[j+2], out.Pix[j+3] = g, g, g, 255
		}
	}
	return out
}
