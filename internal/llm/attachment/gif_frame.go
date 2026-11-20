package attachment

import (
	"encoding/binary"
	"fmt"
	"image"
)

// gifFirstFrameBounds reads the first image descriptor without allocating its
// pixels. gif.DecodeConfig has already validated the logical screen header.
func gifFirstFrameBounds(data []byte, screen image.Config) (image.Rectangle, error) {
	pos := 13
	if data[10]&0x80 != 0 {
		pos += 3 * (2 << (data[10] & 7))
	}
	for pos < len(data) {
		switch data[pos] {
		case 0x21: // Extension label followed by length-prefixed sub-blocks.
			pos += 2
			for pos < len(data) {
				size := int(data[pos])
				pos++
				if size == 0 {
					break
				}
				pos += size
			}
		case 0x2c: // Image descriptor: left, top, width, height, packed fields.
			if len(data)-pos < 10 {
				return image.Rectangle{}, fmt.Errorf("truncated GIF image descriptor")
			}
			fields := data[pos+1:]
			x := int(binary.LittleEndian.Uint16(fields))
			y := int(binary.LittleEndian.Uint16(fields[2:]))
			w := int(binary.LittleEndian.Uint16(fields[4:]))
			h := int(binary.LittleEndian.Uint16(fields[6:]))
			frame := image.Rect(x, y, x+w, y+h)
			if frame.Empty() || !frame.In(image.Rect(0, 0, screen.Width, screen.Height)) {
				return image.Rectangle{}, fmt.Errorf("invalid GIF first-frame bounds %v", frame)
			}
			return frame, nil
		default:
			return image.Rectangle{}, fmt.Errorf("missing GIF first image descriptor")
		}
	}
	return image.Rectangle{}, fmt.Errorf("truncated GIF before first image descriptor")
}
