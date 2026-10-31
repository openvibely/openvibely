package jsonview

import (
	"encoding/json"
	"math"
	"strconv"
	"unicode/utf8"
)

// StringSize counts the default JSON encoding without allocating for valid text.
func StringSize(s string) int { return textSize(s) }

// StringBytesSize counts a decoded UTF-8 byte span without first copying it to a string.
func StringBytesSize(s []byte) int { return textSize(s) }

func textSize[T ~string | ~[]byte](s T) int {
	n := 2
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch c {
			case '"', '\\', '\b', '\f', '\n', '\r', '\t':
				n += 2
			case '<', '>', '&':
				n += 6
			default:
				if c < 0x20 {
					n += 6
				} else {
					n++
				}
			}
			i++
			continue
		}
		r, width := utf8.DecodeRuneInString(string(s[i:]))
		if width == 1 {
			// Let the encoder normalize malformed UTF-8; its replacement spelling
			// can differ between JSON implementations. Valid text stays allocation-free.
			raw, _ := json.Marshal(string(s))
			return len(raw)
		}
		if r == '\u2028' || r == '\u2029' {
			n += 6
		} else {
			n += width
		}
		i += width
	}
	return n
}

func IntegerSize(value int64) int {
	var buffer [32]byte
	return len(strconv.AppendInt(buffer[:0], value, 10))
}

func UnsignedIntegerSize(value uint64) int {
	var buffer [32]byte
	return len(strconv.AppendUint(buffer[:0], value, 10))
}

func FloatSize(value float64, bits int) (int, bool) {
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, false
	}
	format := byte('f')
	absolute := math.Abs(value)
	if absolute != 0 && ((bits == 64 && (absolute < 1e-6 || absolute >= 1e21)) || (bits == 32 && (float32(absolute) < 1e-6 || float32(absolute) >= 1e21))) {
		format = 'e'
	}
	var buffer [64]byte
	encoded := strconv.AppendFloat(buffer[:0], value, format, -1, bits)
	if format == 'e' && len(encoded) >= 4 && encoded[len(encoded)-4] == 'e' && encoded[len(encoded)-3] == '-' && encoded[len(encoded)-2] == '0' {
		encoded = encoded[:len(encoded)-1]
	}
	return len(encoded), true
}

// Size counts decoded JSON shapes, falling back to the encoder for custom types.
// Depth is bounded so cyclic/unsupported values receive normal encoder errors.
func Size(v any) (int, error) { return valueSize(v, 0) }
func valueSize(v any, depth int) (int, error) {
	if depth < 128 {
		switch x := v.(type) {
		case Value:
			return x.Size()
		case json.RawMessage:
			return EncodedSize(x)
		case nil:
			return 4, nil
		case string:
			return StringSize(x), nil
		case bool:
			if x {
				return 4, nil
			}
			return 5, nil
		case float64:
			if n, ok := FloatSize(x, 64); ok {
				return n, nil
			}
		case int:
			return IntegerSize(int64(x)), nil
		case int64:
			return IntegerSize(x), nil
		case uint64:
			return UnsignedIntegerSize(x), nil
		case []any:
			if x == nil {
				return 4, nil
			}
			n := 2
			for i, v := range x {
				if i > 0 {
					n++
				}
				size, err := valueSize(v, depth+1)
				if err != nil {
					return 0, err
				}
				n += size
			}
			return n, nil
		case map[string]any:
			if x == nil {
				return 4, nil
			}
			n := 2
			i := 0
			for k, v := range x {
				if i > 0 {
					n++
				}
				i++
				size, err := valueSize(v, depth+1)
				if err != nil {
					return 0, err
				}
				n += StringSize(k) + 1 + size
			}
			return n, nil
		}
	}
	raw, err := json.Marshal(v)
	return len(raw), err
}
