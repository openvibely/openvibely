package attachment

import (
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	"math"
	"runtime"
)

// Convert straight RGBA one row at a time to preserve hidden colors without copying the full image.
func straightRow(src image.Image, y int, buf *image.NRGBA) []byte {
	b := src.Bounds()
	switch img := src.(type) {
	case *image.NRGBA:
		i := img.PixOffset(b.Min.X, b.Min.Y+y)
		return img.Pix[i : i+b.Dx()*4]
	case *image.NRGBA64:
		// Read the high byte of each straight 16-bit channel, even when alpha is zero.
		i := img.PixOffset(b.Min.X, b.Min.Y+y)
		for j := range buf.Pix {
			buf.Pix[j] = img.Pix[i+2*j]
		}
		return buf.Pix
	case *image.Paletted:
		var colors [256]color.NRGBA
		for i, c := range img.Palette {
			colors[i] = color.NRGBAModel.Convert(c).(color.NRGBA)
		}
		i := img.PixOffset(b.Min.X, b.Min.Y+y)
		for x, index := range img.Pix[i : i+b.Dx()] {
			p := colors[index]
			j := 4 * x
			buf.Pix[j], buf.Pix[j+1], buf.Pix[j+2], buf.Pix[j+3] = p.R, p.G, p.B, p.A
		}
		return buf.Pix
	}
	draw.Draw(buf, buf.Bounds(), src, image.Pt(b.Min.X, b.Min.Y+y), draw.Src)
	return buf.Pix
}

type taps[T float32 | int32] struct {
	start   int
	weights []T
}

// Apply the triangle filter vertically, then horizontally.
func triangleAxis(src, dst int) []taps[float32] {
	axis := make([]taps[float32], dst)
	ratio := float32(src) / float32(dst)
	scale := max(ratio, 1)
	for i := range axis {
		center := float32((float32(i) + 0.5) * ratio)
		lo := max(0, int(math.Floor(float64(center-scale))))
		hi := min(src, int(math.Ceil(float64(center+scale))))
		weights := make([]float32, hi-lo)
		var sum float32
		for k := range weights {
			x := (float32(lo+k) - (center - 0.5)) / scale
			weights[k] = max(0, 1-float32(math.Abs(float64(x))))
			sum += weights[k]
		}
		for k := range weights {
			weights[k] /= sum
		}
		axis[i] = taps[float32]{lo, weights}
	}
	return axis
}

// Go's PNG decoder stores 16-bit grayscale, opaque RGB and straight RGBA
// samples in big-endian order. Keep their native precision through both passes.
func accumulate16BitRows(src image.Image) func(int, float32, []float32) {
	var pix []byte
	var stride, channels int
	switch img := src.(type) {
	case *image.Gray16:
		pix, stride, channels = img.Pix, img.Stride, 1
	case *image.NRGBA64:
		pix, stride, channels = img.Pix, img.Stride, 4
	case *image.RGBA64: // image/png uses this for opaque 16-bit RGB.
		pix, stride, channels = img.Pix, img.Stride, 4
	default:
		return nil
	}
	return func(y int, weight float32, row []float32) {
		data := pix[y*stride:]
		for i := 0; i < len(row); i += 4 {
			if channels == 1 {
				v := float32(float32(binary.BigEndian.Uint16(data)) * weight)
				row[i] += v
				row[i+1] += v
				row[i+2] += v
				row[i+3] += float32(65535 * weight)
			} else {
				for c := range 4 {
					row[i+c] += float32(float32(binary.BigEndian.Uint16(data[2*c:])) * weight)
				}
			}
			data = data[2*channels:]
		}
	}
}

func resizeTriangle(src image.Image, w, h int) *image.NRGBA {
	b := src.Bounds()
	xs, ys := triangleAxis(b.Dx(), w), triangleAxis(b.Dy(), h)
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	buf := image.NewNRGBA(image.Rect(0, 0, b.Dx(), 1))
	row := make([]float32, b.Dx()*4)
	add16 := accumulate16BitRows(src)
	for y, yt := range ys {
		clear(row)
		for k, weight := range yt.weights {
			if weight == 0 {
				continue
			}
			if add16 != nil {
				add16(yt.start+k, weight, row)
				continue
			}
			for j, v := range straightRow(src, yt.start+k, buf) {
				// Round the product before adding to prevent a fused multiply-add.
				row[j] += float32(float32(v) * weight)
			}
		}
		for x, xt := range xs {
			var sums [4]float32
			for k, weight := range xt.weights {
				i := 4 * (xt.start + k)
				for c := range sums {
					sums[c] += float32(row[i+c] * weight)
				}
			}
			i := out.PixOffset(x, y)
			for c, v := range sums {
				if add16 != nil {
					// Round to 16 bits before converting to 8 bits.
					sample := int(math.Round(float64(min(65535, max(0, v)))))
					out.Pix[i+c] = byte((sample + 128) / 257)
				} else {
					out.Pix[i+c] = byte(math.Round(float64(min(255, max(0, v)))))
				}
			}
		}
	}
	return out
}

// Keep enough weight precision to round straight color and alpha independently
// at the image edges. Each Lanczos pass produces eight-bit channels.
const lanczosOne = 1 << 20

func lanczosAxis(src, dst int, portable bool) ([]taps[int32], int) {
	axis := make([]taps[int32], dst)
	scale := float64(dst) / float64(src)
	fscale := min(1, scale)
	support := min(3/fscale, 127)
	maxSpan := 0
	for i := range axis {
		center := (float64(i)+0.5)/scale - 0.5
		lo := int(math.Floor(center - support + 0.5))
		hi := int(math.Floor(center + support + 0.5))
		// The portable kernel clips the source window before normalizing it.
		if portable {
			lo, hi = max(0, lo), min(src-1, hi)
		}
		n := hi - lo + 1
		var raw [256]float64
		var total float64
		for k := 0; k < n; k++ {
			x := math.Abs(float64(lo+k)-center) * fscale
			switch {
			case x == 0 || (portable && x < 1e-8):
				raw[k] = 1
			case x < 3:
				t := math.Pi * x
				raw[k] = math.Sin(t) / t * math.Sin(t/3) / (t / 3)
			}
			total += raw[k]
		}
		weights := make([]int32, n)
		big, sum := 0, 0
		for k := range weights {
			var v int
			if portable {
				// Use 14-bit coefficients, expressed in the shared accumulator units.
				const one = 1 << 14
				v = int(math.RoundToEven(raw[k]*(1/total)*one)) * (lanczosOne / one)
			} else {
				v = int(math.RoundToEven(raw[k] / total * lanczosOne))
			}
			weights[k] = int32(v)
			sum += v
			if math.Abs(float64(v)) > math.Abs(float64(weights[big])) {
				big = k
			}
		}
		// Preserve mirror symmetry when the output pixel is centered between
		// two source pixels. Assigning the entire rounding residual to one
		// center tap biases alternating colors at half-integer averages.
		residual := int32(lanczosOne - sum)
		if !portable && big+1 < n && raw[big] == raw[big+1] {
			weights[big+1] += residual / 2
			residual -= residual / 2
		}
		weights[big] += residual
		// Extend the edge pixels for taps outside the image.
		first, last := max(0, lo), min(src-1, hi)
		if first != lo || last != hi {
			clipped := make([]int32, last-first+1)
			for k, v := range weights {
				j := min(last, max(first, lo+k))
				clipped[j-first] += v
			}
			weights = clipped
		}
		axis[i] = taps[int32]{first, weights}
		maxSpan = max(maxSpan, len(weights))
	}
	return axis, maxSpan
}

func horizontalLanczosPixel(data []byte, start int, weights []int32, dst []byte) {
	var sums [4]int32
	for k, weight := range weights {
		i := start + k*4
		for c := range sums {
			sums[c] += int32(data[i+c]) * int32(weight)
		}
	}
	for c, v := range sums {
		dst[c] = byte(min(255, max(0, (v+lanczosOne/2)/lanczosOne)))
	}
}

func resizeLanczos(src image.Image, w, h int) *image.NRGBA {
	return resizeLanczosPixels(src, w, h, runtime.GOOS != "darwin")
}

func resizeLanczosPixels(src image.Image, w, h int, portable bool) *image.NRGBA {
	b := src.Bounds()
	xs, _ := lanczosAxis(b.Dx(), w, portable)
	ys, maxSpan := lanczosAxis(b.Dy(), h, portable)
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	buf := image.NewNRGBA(image.Rect(0, 0, b.Dx(), 1))
	// Only horizontal rows still needed by the vertical filter are retained.
	rows := make([]byte, maxSpan*out.Stride)
	next := 0
	sums := make([]int32, out.Stride)
	for y, yt := range ys {
		for next = max(next, yt.start); next < yt.start+len(yt.weights); next++ {
			row := straightRow(src, next, buf)
			slot := (next % maxSpan) * out.Stride
			for x, xt := range xs {
				horizontalLanczosPixel(row, xt.start*4, xt.weights, rows[slot+4*x:])
			}
		}
		clear(sums)
		for k, weight := range yt.weights {
			slot := ((yt.start + k) % maxSpan) * out.Stride
			for i, v := range rows[slot : slot+out.Stride] {
				sums[i] += int32(weight) * int32(v)
			}
		}
		for i, v := range sums {
			out.Pix[y*out.Stride+i] = byte(min(255, max(0, (v+lanczosOne/2)/lanczosOne)))
		}
	}
	return out
}
