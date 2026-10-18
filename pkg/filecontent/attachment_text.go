package filecontent

import (
	"bytes"
	"encoding/binary"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/unicode/utf32"
)

// DecodeAttachmentText leaves stored/downloaded bytes untouched. Detect UTF-16
// before checking NUL bytes, since those are normal in UTF-16 text.
func DecodeAttachmentText(data []byte) (string, bool) {
	// UTF-32 must be checked before the overlapping little-endian UTF-16 BOM.
	if endian, ok := attachmentUTF32(data); ok {
		if len(data)%4 != 0 {
			return "", false
		}
		var order binary.ByteOrder = binary.BigEndian
		if endian == utf32.LittleEndian {
			order = binary.LittleEndian
		}
		for i := 4; i < len(data); i += 4 {
			r := order.Uint32(data[i : i+4])
			if r > utf8.MaxRune || r >= 0xd800 && r <= 0xdfff {
				return "", false
			}
		}
		decoded, err := utf32.UTF32(endian, utf32.UseBOM).NewDecoder().Bytes(data)
		return string(decoded), err == nil && !bytes.ContainsRune(decoded, 0)
	}
	var order binary.ByteOrder
	if bytes.HasPrefix(data, []byte{0xff, 0xfe}) {
		order, data = binary.LittleEndian, data[2:]
	} else if bytes.HasPrefix(data, []byte{0xfe, 0xff}) {
		order, data = binary.BigEndian, data[2:]
	}

	if order != nil {
		if len(data)%2 != 0 {
			return "", false
		}
		units := make([]uint16, len(data)/2)
		for i := range units {
			units[i] = order.Uint16(data[2*i:])
		}
		for i := 0; i < len(units); i++ {
			u := units[i]
			if u >= 0xd800 && u <= 0xdbff {
				if i+1 >= len(units) || units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
					return "", false
				}
				i++
			} else if u >= 0xdc00 && u <= 0xdfff {
				return "", false
			}
		}
		text := string(utf16.Decode(units))
		return text, !strings.ContainsRune(text, 0)
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if bytes.ContainsRune(data, 0) {
		return "", false
	}
	if utf8.Valid(data) {
		return string(data), true
	}
	// No statistical guessing or legacy single-byte fallback.
	return "", false
}

// Recognize explicit UTF-32 byte order marks.
func attachmentUTF32(data []byte) (utf32.Endianness, bool) {
	if len(data) < 4 {
		return utf32.BigEndian, false
	}
	if bytes.HasPrefix(data, []byte{0, 0, 0xfe, 0xff}) {
		return utf32.BigEndian, true
	}
	if bytes.HasPrefix(data, []byte{0xff, 0xfe, 0, 0}) {
		return utf32.LittleEndian, true
	}
	return utf32.BigEndian, false
}
