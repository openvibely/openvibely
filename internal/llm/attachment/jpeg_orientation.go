package attachment

import (
	"bytes"
	"encoding/binary"
	"io"
	"iter"
)

func jpegMetadataWithFullEXIF(data []byte) []byte {
	var exif []byte
	for marker, payload := range jpegSegments(data) {
		if marker == 0xe1 && bytes.HasPrefix(payload, []byte("Exif\x00\x00")) {
			exif = binary.BigEndian.AppendUint16([]byte{0xff, marker}, uint16(len(payload)+2))
			exif = append(exif, payload...)
			break
		}
	}
	return append(exif, jpegProfileSegments(data, true)...)
}

func jpegRenderMetadata(data []byte) []byte {
	return append(jpegOrientationSegment(data), jpegProfileSegments(data, false)...)
}

// jpegOrientationSegment returns a minimal EXIF APP1 segment containing only
// the main image's orientation. Thumbnails and their dimensions describe the
// original file and should not be copied to the resized image.
func jpegOrientationSegment(data []byte) []byte {
	for marker, payload := range jpegSegments(data) {
		if marker == 0xe1 && bytes.HasPrefix(payload, []byte("Exif\x00\x00")) {
			if orientation := tiffOrientation(payload[6:]); orientation != 0 {
				// Big-endian TIFF, first directory at offset 8, one inline SHORT entry.
				return []byte{0xff, 0xe1, 0, 34, 'E', 'x', 'i', 'f', 0, 0, 'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, orientation, 0, 0, 0, 0, 0, 0}
			}
		}
	}
	return nil
}

func tiffOrientation(tiff []byte) byte {
	if len(tiff) < 8 {
		return 0
	}
	var order binary.ByteOrder
	switch string(tiff[:4]) {
	case "II\x2a\x00":
		order = binary.LittleEndian
	case "MM\x00\x2a":
		order = binary.BigEndian
	default:
		return 0
	}
	offset := order.Uint32(tiff[4:8])
	if offset < 8 || uint64(offset) > uint64(len(tiff)-2) {
		return 0
	}
	directory := tiff[offset:]
	count := int(order.Uint16(directory))
	entries := directory[2:]
	if count > len(entries)/12 {
		return 0
	}
	for i := 0; i < count; i++ {
		entry := entries[i*12:]
		if order.Uint16(entry) == 0x0112 && order.Uint16(entry[2:]) == 3 && order.Uint32(entry[4:]) == 1 {
			value := order.Uint16(entry[8:])
			if value >= 1 && value <= 8 {
				return byte(value)
			}
		}
	}
	return 0
}

// jpegSegments walks length-prefixed markers before the first scan.
func jpegSegments(data []byte) iter.Seq2[byte, []byte] {
	return func(yield func(byte, []byte) bool) {
		if !bytes.HasPrefix(data, []byte{0xff, 0xd8}) {
			return
		}
		data = data[2:]
		for len(data) >= 4 && data[0] == 0xff {
			marker := data[1]
			switch marker {
			case 0xff:
				data = data[1:]
				continue // padding between markers
			case 0xda, 0xd9:
				return // start of scan or end of image
			case 0x01, 0xd0, 0xd1, 0xd2, 0xd3, 0xd4, 0xd5, 0xd6, 0xd7:
				data = data[2:]
				continue
			}
			length := int(binary.BigEndian.Uint16(data[2:4]))
			data = data[2:]
			if length < 2 || length > len(data) {
				return
			}
			segment := data[:length]
			if !yield(marker, segment[2:]) {
				return
			}
			data = data[length:]
		}
	}
}

// JPEG ICC profiles can span multiple APP2 segments stored out of order.
func jpegProfileSegments(data []byte, rgbOnly bool) []byte {
	var parts [][]byte
	for marker, payload := range jpegSegments(data) {
		if marker != 0xe2 || !bytes.HasPrefix(payload, []byte("ICC_PROFILE\x00")) {
			continue
		}
		if len(payload) < 14 {
			return nil
		}
		seq, total := int(payload[12]), int(payload[13])
		if seq < 1 || seq > total {
			return nil
		}
		if parts == nil {
			parts = make([][]byte, total)
		}
		if total != len(parts) || parts[seq-1] != nil {
			return nil
		}
		parts[seq-1] = payload
	}
	var header [20]byte
	n := 0
	for _, part := range parts {
		if part == nil {
			return nil
		}
		n += copy(header[n:], part[14:])
	}
	// Re-encoded JPEGs have no CMYK channels for the source profile to describe.
	space := string(header[16:20])
	if n < len(header) || space == "CMYK" || (rgbOnly && space != "RGB ") {
		return nil
	}
	var result []byte
	for _, part := range parts {
		result = append(result, 0xff, 0xe2)
		result = binary.BigEndian.AppendUint16(result, uint16(len(part)+2))
		result = append(result, part...)
	}
	return result
}

// JPEG writes an ICC profile in numbered APP2 segments regardless of the
// container it was decoded from. Include these bytes in every quality trial.
func metadataForJPEG(data []byte, format string) []byte {
	const segmentSize = 65535 - 2 - 14 // length field, ICC signature and numbering
	const maxProfile = 255 * segmentSize
	var profile []byte
	switch format {
	case "jpeg":
		return jpegRenderMetadata(data)
	case "png":
		for chunk := range pngChunks(data) {
			if string(chunk[4:8]) != "iCCP" {
				continue
			}
			r, err := pngProfileReader(chunk[8 : len(chunk)-4])
			if err != nil {
				return nil
			}
			profile, err = io.ReadAll(io.LimitReader(r, maxProfile+1))
			r.Close()
			if err != nil {
				return nil
			}
			break
		}
	case "webp":
		chunk, _ := webpMetadata(data)
		if chunk != nil {
			profile = chunk[8 : 8+binary.LittleEndian.Uint32(chunk[4:8])]
		}
	}
	if len(profile) < 20 || len(profile) > maxProfile {
		return nil
	}
	total := (len(profile) + segmentSize - 1) / segmentSize
	result := make([]byte, 0, len(profile)+18*total)
	for i := 1; i <= total; i++ {
		n := min(len(profile), segmentSize)
		result = append(result, 0xff, 0xe2)
		result = binary.BigEndian.AppendUint16(result, uint16(n+16))
		result = append(result, "ICC_PROFILE\x00"...)
		result = append(result, byte(i), byte(total))
		result = append(result, profile[:n]...)
		profile = profile[n:]
	}
	return result
}
