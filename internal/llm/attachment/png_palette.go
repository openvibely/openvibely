package attachment

import (
	"image"
	"image/color"
)

// Split straight RGBA pixels at the median of the widest channel.
type palettePixel [4]uint8

type paletteBox struct {
	pixels  []palettePixel
	channel int
	span    int
	mean    color.NRGBA
}

func measurePaletteBox(pixels []palettePixel) paletteBox {
	low := palettePixel{255, 255, 255, 255}
	var high palettePixel
	var sum [4]uint64
	for _, p := range pixels {
		for c, v := range p {
			low[c] = min(low[c], v)
			high[c] = max(high[c], v)
			sum[c] += uint64(v)
		}
	}
	box := paletteBox{pixels: pixels}
	var mean palettePixel
	count := uint64(len(pixels))
	for c := range mean {
		span := int(high[c] - low[c])
		if span > box.span {
			box.span, box.channel = span, c
		}
		mean[c] = uint8((sum[c] + count/2) / count)
	}
	box.mean = color.NRGBA{R: mean[0], G: mean[1], B: mean[2], A: mean[3]}
	return box
}

func (box paletteBox) split(scratch []palettePixel) (paletteBox, paletteBox) {
	// Counting sort is linear for an eight-bit channel and retains equal-channel order.
	var offsets [256]int
	for _, p := range box.pixels {
		offsets[p[box.channel]]++
	}
	start := 0
	for i, count := range offsets {
		offsets[i] = start
		start += count
	}
	for _, p := range box.pixels {
		v := p[box.channel]
		scratch[offsets[v]] = p
		offsets[v]++
	}
	copy(box.pixels, scratch[:len(box.pixels)])
	mid := len(box.pixels) / 2
	return measurePaletteBox(box.pixels[:mid]), measurePaletteBox(box.pixels[mid:])
}

func nearestPaletteIndex(p color.NRGBA, palette []color.NRGBA) uint8 {
	bestDistance := 4*255*255 + 1
	var best uint8
	for i, q := range palette {
		r, g, b, a := int(p.R)-int(q.R), int(p.G)-int(q.G), int(p.B)-int(q.B), int(p.A)-int(q.A)
		distance := r*r + g*g + b*b + a*a
		if distance < bestDistance {
			best, bestDistance = uint8(i), distance
			if distance == 0 {
				break
			}
		}
	}
	return best
}

func buildPNGPalette(src image.Image) *image.Paletted {
	bounds := src.Bounds()
	indexed := image.NewPaletted(bounds, nil)
	exact := make(map[color.NRGBA]uint8)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			p := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
			index, ok := exact[p]
			if !ok {
				if len(exact) == 256 {
					return quantizePNG(src, indexed)
				}
				index = uint8(len(exact))
				exact[p] = index
				indexed.Palette = append(indexed.Palette, p)
			}
			indexed.SetColorIndex(x, y, index)
		}
	}
	return indexed
}

func quantizePNG(src image.Image, indexed *image.Paletted) *image.Paletted {
	bounds := src.Bounds()
	pixels := make([]palettePixel, 0, len(indexed.Pix))
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			p := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
			pixels = append(pixels, palettePixel{p.R, p.G, p.B, p.A})
		}
	}
	boxes := []paletteBox{measurePaletteBox(pixels)}
	scratch := make([]palettePixel, len(pixels))
	for len(boxes) < 256 {
		pick := 0
		for i := range boxes {
			if boxes[i].span > boxes[pick].span {
				pick = i
			}
		}
		if boxes[pick].span == 0 {
			break
		}
		left, right := boxes[pick].split(scratch)
		boxes[pick] = left
		boxes = append(boxes, right)
	}
	palette := make([]color.NRGBA, len(boxes))
	indexed.Palette = make(color.Palette, len(boxes))
	for i, box := range boxes {
		palette[i] = box.mean
		indexed.Palette[i] = box.mean
	}
	// Cache repeated diagram colors, with bounded memory for photographic inputs.
	indices := make(map[color.NRGBA]uint8)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			p := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
			i, ok := indices[p]
			if !ok {
				i = nearestPaletteIndex(p, palette)
				if len(indices) < 65536 {
					indices[p] = i
				}
			}
			indexed.SetColorIndex(x, y, i)
		}
	}
	return indexed
}
