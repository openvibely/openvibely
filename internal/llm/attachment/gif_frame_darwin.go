package attachment

import (
	"image"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

type frameReader struct {
	createData                 func(uintptr, *byte, int) uintptr
	createSource               func(uintptr, uintptr) uintptr
	createImage                func(uintptr, uintptr, uintptr) uintptr
	width, height, stride, bpp func(uintptr) uintptr
	bitmapInfo                 func(uintptr) uint32
	provider, copyData         func(uintptr) uintptr
	length                     func(uintptr) int
	bytes                      func(uintptr) *byte
	release                    func(uintptr)
}

var systemFrameReader = sync.OnceValue(func() *frameReader {
	lib, err := purego.Dlopen("/System/Library/Frameworks/ImageIO.framework/ImageIO", purego.RTLD_LAZY|purego.RTLD_LOCAL)
	if err != nil {
		return nil
	}
	var r frameReader
	for symbol, fn := range map[string]any{
		"CFDataCreate":                    &r.createData,
		"CGImageSourceCreateWithData":     &r.createSource,
		"CGImageSourceCreateImageAtIndex": &r.createImage,
		"CGImageGetWidth":                 &r.width,
		"CGImageGetHeight":                &r.height,
		"CGImageGetBitsPerPixel":          &r.bpp,
		"CGImageGetBytesPerRow":           &r.stride,
		"CGImageGetBitmapInfo":            &r.bitmapInfo,
		"CGImageGetDataProvider":          &r.provider,
		"CGDataProviderCopyData":          &r.copyData,
		"CFDataGetLength":                 &r.length,
		"CFDataGetBytePtr":                &r.bytes,
		"CFRelease":                       &r.release,
	} {
		addr, err := purego.Dlsym(lib, symbol)
		if err != nil {
			return nil
		}
		purego.RegisterFunc(fn, addr)
	}
	return &r
})

// Read the first displayed frame in its native pixel layout. Unrecognized
// layouts fall back to the portable GIF decoder.
func nativeGIFFrame(data []byte, w, h int) image.Image {
	r := systemFrameReader()
	if r == nil {
		return nil
	}
	input := r.createData(0, &data[0], len(data))
	if input == 0 {
		return nil
	}
	defer r.release(input)
	source := r.createSource(input, 0)
	if source == 0 {
		return nil
	}
	defer r.release(source)
	frame := r.createImage(source, 0, 0)
	if frame == 0 {
		return nil
	}
	defer r.release(frame)
	const alphaLast = 3
	const skipLast = 5
	info := r.bitmapInfo(frame)
	if r.width(frame) != uintptr(w) || r.height(frame) != uintptr(h) || r.bpp(frame) != 32 || (info != alphaLast && info != skipLast) {
		return nil
	}
	provider := r.provider(frame)
	if provider == 0 {
		return nil
	}
	buf := r.copyData(provider)
	if buf == 0 {
		return nil
	}
	defer r.release(buf)
	stride := int(r.stride(frame))
	length := r.length(buf)
	if stride < 4*w || stride > length/h {
		return nil
	}
	start := r.bytes(buf)
	if start == nil {
		return nil
	}
	pixels := unsafe.Slice(start, length)
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		copy(out.Pix[y*out.Stride:(y+1)*out.Stride], pixels[y*stride:y*stride+4*w])
	}
	if info == skipLast {
		for i := 3; i < len(out.Pix); i += 4 {
			out.Pix[i] = 255
		}
	}
	return out
}
