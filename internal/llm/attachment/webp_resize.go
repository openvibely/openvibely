package attachment

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/draw"
	"io"
	"iter"

	"github.com/gen2brain/webp"
	staticwebp "golang.org/x/image/webp"
)

func resizeLosslessWebP(input InputFile, width, height int, animated bool) (InputFile, error) {
	reader := bytes.NewReader(input.Data)
	var frame image.Image
	var err error
	if animated {
		frame, err = firstWebPFrame(input.Data)
	} else {
		frame, err = staticwebp.Decode(reader)
	}
	if err != nil {
		return input, err
	}
	if !animated {
		frame = packedWebP(input.Data, frame)
	}
	still := resizeTriangle(frame, width, height)
	// A partial first frame can leave transparent pixels on an opaque canvas.
	if animated && input.Data[20]&0x10 == 0 {
		discardAlpha(still)
	}
	var out bytes.Buffer
	if err := webp.Encode(&out, still, webp.Options{Lossless: true, Exact: true, Method: webp.DefaultMethod}); err != nil {
		return input, err
	}
	input.Data = preserveWebPMetadata(input.Data, out.Bytes(), width, height, true)
	input.MediaType = "image/webp"
	return input, nil
}

// Decode the color frame separately to preserve straight RGB at low opacity.
func firstWebPFrame(data []byte) (image.Image, error) {
	invalid := errors.New("invalid animated WebP frame")
	if len(data) < 30 {
		return nil, invalid
	}
	uint24 := func(b []byte) int { return int(b[0]) | int(b[1])<<8 | int(b[2])<<16 }
	canvas := image.Rect(0, 0, 1+uint24(data[24:]), 1+uint24(data[27:]))
	for chunk := range webpChunks(data) {
		if string(chunk[:4]) != "ANMF" {
			continue
		}
		size := int(binary.LittleEndian.Uint32(chunk[4:8]))
		if size < 24 {
			return nil, invalid
		}
		origin := image.Pt(2*uint24(chunk[8:]), 2*uint24(chunk[11:]))
		bounds := image.Rect(0, 0, 1+uint24(chunk[14:]), 1+uint24(chunk[17:]))
		if !bounds.Add(origin).In(canvas) {
			return nil, invalid
		}
		payload := chunk[24 : 8+size]
		header := []byte("RIFF\x00\x00\x00\x00WEBP")
		if string(payload[:4]) == "ALPH" {
			// Standalone lossy images with a separate alpha plane require VP8X.
			header = append(header, []byte("VP8X\x0a\x00\x00\x00\x10\x00\x00\x00")...)
			header = append(header, chunk[14:20]...)
		}
		binary.LittleEndian.PutUint32(header[4:8], uint32(len(header)-8+len(payload)))
		still := append(header, payload...)
		cfg, err := staticwebp.DecodeConfig(bytes.NewReader(still))
		if err != nil || cfg.Width != bounds.Dx() || cfg.Height != bounds.Dy() {
			return nil, invalid
		}
		img, err := staticwebp.Decode(bytes.NewReader(still))
		if err != nil {
			return nil, err
		}
		frame := packedWebP(still, img)
		blend := chunk[23]&2 == 0 && string(payload[:4]) != "VP8 "
		if bounds == canvas && !blend {
			return frame, nil
		}
		out, ok := frame.(*image.NRGBA)
		region := bounds.Add(origin)
		if !ok || bounds != canvas {
			out = image.NewNRGBA(canvas)
			draw.Draw(out, region, frame, bounds.Min, draw.Src)
		}
		if blend {
			blendOnClearCanvas(out, region)
		}
		return out, nil
	}
	return nil, invalid
}

// The first frame blends over transparent black. WebP compositing uses a
// 24-bit fixed-point reciprocal when converting back to straight RGB.
func blendOnClearCanvas(canvas *image.NRGBA, region image.Rectangle) {
	for y := region.Min.Y; y < region.Max.Y; y++ {
		start := canvas.PixOffset(region.Min.X, y)
		row := canvas.Pix[start : start+4*region.Dx()]
		for i := 0; i < len(row); i += 4 {
			alpha := uint32(row[i+3])
			if alpha == 0 {
				clear(row[i : i+3])
				continue
			}
			scale := alpha * ((1 << 24) / alpha)
			for c := range 3 {
				row[i+c] = byte(uint32(row[i+c]) * scale >> 24)
			}
		}
	}
}

// RIFF chunk sizes exclude the padding byte for odd lengths.
func webpChunks(data []byte) iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {
		for chunks := data[12:]; len(chunks) >= 8; {
			size := uint64(binary.LittleEndian.Uint32(chunks[4:8]))
			end := 8 + size + (size & 1)
			if end > uint64(len(chunks)) || !yield(chunks[:end]) {
				return
			}
			chunks = chunks[end:]
		}
	}
}

func webpMetadata(original []byte) (icc, exif []byte) {
	for chunk := range webpChunks(original) {
		switch string(chunk[:4]) {
		case "ICCP":
			if binary.LittleEndian.Uint32(chunk[4:8]) >= 20 {
				icc = chunk
			}
		case "EXIF":
			exif = chunk
		}
	}
	return
}

// A lossy WebP stores its color frame separately from its alpha plane. Decode
// the opaque frame with libwebp's RGB conversion before restoring straight alpha.
// Decoding both together to premultiplied RGBA loses colors at low opacity.
func packedWebP(data []byte, decoded image.Image) image.Image {
	var alpha *image.NYCbCrA
	switch img := decoded.(type) {
	case *image.NYCbCrA:
		alpha = img
	case *image.YCbCr:
	default:
		return decoded // lossless WebP already supplies packed pixels
	}
	b := decoded.Bounds()
	if b.Dx() > maxPackedImageBytes/4/b.Dy() {
		return decoded
	}
	for chunk := range webpChunks(data) {
		if string(chunk[:4]) != "VP8 " {
			continue
		}
		header := []byte("RIFF\x00\x00\x00\x00WEBP")
		binary.LittleEndian.PutUint32(header[4:8], uint32(4+len(chunk)))
		frame, err := webp.DecodeAll(io.MultiReader(bytes.NewReader(header), bytes.NewReader(chunk)))
		if err != nil || len(frame.Image) != 1 {
			return decoded
		}
		rgb, ok := frame.Image[0].(*image.RGBA)
		if !ok || rgb.Bounds() != b {
			return decoded
		}
		out := &image.NRGBA{Pix: rgb.Pix, Stride: rgb.Stride, Rect: b}
		if alpha != nil {
			for y := b.Min.Y; y < b.Max.Y; y++ {
				dst := out.Pix[out.PixOffset(b.Min.X, y):]
				start := alpha.AOffset(b.Min.X, y)
				for x, a := range alpha.A[start : start+b.Dx()] {
					dst[4*x+3] = a
				}
			}
		}
		return out
	}
	return decoded
}

func preserveWebPMetadata(original, encoded []byte, width, height int, keepEXIFAndRGBProfiles bool) []byte {
	icc, exif := webpMetadata(original)
	if keepEXIFAndRGBProfiles && icc != nil && string(icc[24:28]) != "RGB " {
		icc = nil
	}
	if !keepEXIFAndRGBProfiles {
		exif = nil
	}
	if icc == nil && exif == nil {
		return encoded
	}
	// Lossy images with alpha already have a VP8X header. Simple VP8 and
	// VP8L images need one before extended chunks can be added.
	var result []byte
	chunks := encoded[12:]
	if string(chunks[:4]) == "VP8X" {
		result = bytes.Clone(encoded[:30])
		chunks = encoded[30:]
	} else {
		result = make([]byte, 30)
		copy(result, "RIFF")
		copy(result[8:], "WEBPVP8X")
		result[16] = 10
		if string(chunks[:4]) == "VP8L" {
			result[20] = encoded[24] & 0x10
		}
	}
	if icc != nil {
		result[20] |= 0x20
	}
	if exif != nil {
		result[20] |= 0x08
	}
	// Prepared sides are at most 6000, so their 24-bit high bytes are zero.
	binary.LittleEndian.PutUint16(result[24:26], uint16(width-1))
	binary.LittleEndian.PutUint16(result[27:29], uint16(height-1))
	result = append(result, icc...)
	result = append(result, chunks...)
	result = append(result, exif...)
	binary.LittleEndian.PutUint32(result[4:8], uint32(len(result)-8))
	return result
}
