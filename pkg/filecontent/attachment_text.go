package filecontent

import (
	"bytes"
	"encoding/binary"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/saintfish/chardet"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/ianaindex"
	"golang.org/x/text/encoding/unicode/utf32"
)

// DecodeAttachmentText leaves stored/downloaded bytes untouched. Detect UTF-16
// before checking NUL bytes, since those are normal in UTF-16 text.
func DecodeAttachmentText(data []byte) (string, bool) {
	// UTF-32 must be checked before the overlapping little-endian UTF-16 BOM.
	if endian, ok := attachmentUTF32(data); ok {
		decoded, err := utf32.UTF32(endian, utf32.UseBOM).NewDecoder().Bytes(data)
		return string(decoded), err == nil && !bytes.ContainsRune(decoded, 0)
	}
	var order binary.ByteOrder
	if bytes.HasPrefix(data, []byte{0xff, 0xfe}) {
		order, data = binary.LittleEndian, data[2:]
	} else if bytes.HasPrefix(data, []byte{0xfe, 0xff}) {
		order, data = binary.BigEndian, data[2:]
	} else if len(data) >= 4 && len(data)%2 == 0 {
		// The same zero-byte pattern used by text readers for BOM-less UTF-16.
		even, odd, pairs := 0, 0, min(len(data)/2, 512)
		for i := 0; i < pairs; i++ {
			if data[2*i] == 0 {
				even++
			}
			if data[2*i+1] == 0 {
				odd++
			}
		}
		if odd*2 > pairs && even == 0 {
			order = binary.LittleEndian
		}
		if even*2 > pairs && odd == 0 {
			order = binary.BigEndian
		}
	}
	if order != nil {
		if len(data)%2 != 0 {
			return "", false
		}
		units := make([]uint16, len(data)/2)
		for i := range units {
			units[i] = order.Uint16(data[2*i:])
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
	// Reject binary-looking input before statistical detection or Latin-1 fallback.
	sample := data[:min(len(data), 4096)]
	controls := 0
	for _, b := range sample {
		if b < 32 && b != '\t' && b != '\r' && b != '\n' {
			controls++
		}
	}
	if len(sample) > 0 && controls*100 >= len(sample)*5 {
		return "", false
	}
	if result, err := chardet.NewTextDetector().DetectBest(data[:min(len(data), 64<<10)]); err == nil && result.Confidence > 70 {
		if enc, err := ianaindex.MIME.Encoding(result.Charset); err == nil && enc != nil {
			if decoded, err := enc.NewDecoder().Bytes(data); err == nil {
				return string(decoded), true
			}
		}
	}
	decoded, err := charmap.ISO8859_1.NewDecoder().Bytes(data)
	return string(decoded), err == nil
}

// Recognize byte order marks and the zero padding of ASCII-heavy UTF-32 text.
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
	if len(data)%4 != 0 {
		return utf32.BigEndian, false
	}
	count := min(len(data)/4, 50)
	le, be := 0, 0
	printable := func(b byte) bool { return b >= 32 && b <= 126 || b == '\t' || b == '\r' || b == '\n' }
	for i := 0; i < count; i++ {
		p := data[i*4 : i*4+4]
		if p[1] == 0 && p[2] == 0 && p[3] == 0 && printable(p[0]) {
			le++
		}
		if p[0] == 0 && p[1] == 0 && p[2] == 0 && printable(p[3]) {
			be++
		}
	}
	if le*5 > count*4 {
		return utf32.LittleEndian, true
	}
	return utf32.BigEndian, be*5 > count*4
}
