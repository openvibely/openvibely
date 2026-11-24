package computerrecording

import (
	"encoding/binary"
	"fmt"
	"testing"
)

func TestInputAddsTextPreview(t *testing.T) {
	for _, extended := range []bool{false, true} {
		t.Run(fmt.Sprint(extended), func(t *testing.T) {
			key := func(k uint32, down bool) []byte {
				b := make([]byte, 8)
				b[0] = 4
				if down {
					b[1] = 1
				}
				binary.BigEndian.PutUint32(b[4:], k)
				if extended {
					b = append([]byte{255, 0, 0, b[1]}, b[4:]...)
					b = append(b, 0, 0, 0, 0)
				}
				return b
			}
			callbacks, typed := 0, 0
			p := Input{Action: func(string) { callbacks++ }, ContinueAction: func(string) { callbacks++ }, Backspace: func() { callbacks++ }, Unsupported: func() { callbacks++ }, Security: func() InputSecurity { callbacks++; return InputUnknown }, Text: func(rune, InputSecurity) { typed++; callbacks++ }}
			check := func(data []byte, want bool) {
				t.Helper()
				before := callbacks
				for i := 0; i < 2; i++ {
					if got := p.AddsText(data); got != want {
						t.Fatalf("preview %x = %v, want %v", data, got, want)
					}
				}
				if callbacks != before {
					t.Fatal("preview invoked recording callbacks")
				}
				beforeTyped := typed
				p.Feed(data)
				if got := typed > beforeTyped; got != want {
					t.Fatalf("preview disagrees with Feed for %x", data)
				}
			}
			check([]byte("RFB 003."), false)
			check(append([]byte("008\n"), 1, 1), false)
			check(key(0xff54, true), false)
			check([]byte{5, 0, 0, 10, 0, 20, 5, 1, 0, 10, 0, 20, 5, 0, 0, 10, 0, 20, 5, 8, 0, 10, 0, 20}, false)
			check(key('a', true), true)
			check(key('a', false), false)
			check(key(0xffe3, true), false)
			check(key('v', true), false)
			check(key(0xffe3, false), false)
			check(append(append(key(0xffe3, true), key('c', true)...), key(0xffe3, false)...), false)
			check(key('c', true), true)
			check(key(0xffe1, true), false)
			check(key(0xff8d, true), true)
			check(key(0xffe1, false), false)
			check(key(0xff08, true), false)
			fragment := key(0x0101f642, true)
			check(fragment[:5], false)
			check(fragment[5:], true)
			check(key(0xffb1, true), true)
			// Clipboard bytes can look like a key message; they are not typing.
			payload := key('x', true)
			clipboard := []byte{6, 0, 0, 0, 0, 0, 0, byte(len(payload))}
			check(append(clipboard, payload...), false)
		})
	}
}

func clipboardPreviewMessage(size int) []byte {
	b := make([]byte, 8+size)
	b[0] = 6
	binary.BigEndian.PutUint32(b[4:8], uint32(size))
	// Payload deliberately resembles key events.
	for i := 8; i+8 <= len(b); i += 8 {
		copy(b[i:], []byte{4, 1, 0, 0, 0, 0, 0, 'x'})
	}
	return b
}

func TestInputPreviewFragmentedClipboard(t *testing.T) {
	for split := 1; split <= 8; split++ {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			var text []rune
			callbacks := 0
			p := Input{phase: 3, Text: func(r rune, _ InputSecurity) { text = append(text, r); callbacks++ }, Action: func(string) { callbacks++ }, Unsupported: func() { callbacks++ }}
			message := clipboardPreviewMessage(1 << 20)
			check := func(frame []byte, want bool) {
				t.Helper()
				before := callbacks
				for i := 0; i < 2; i++ {
					if got := p.AddsText(frame); got != want {
						t.Fatalf("preview = %v, want %v", got, want)
					}
				}
				if callbacks != before {
					t.Fatal("preview invoked callbacks")
				}
				p.Feed(frame)
				if p.disabled {
					t.Fatal("decoder disabled on valid clipboard")
				}
			}
			check(message[:split], false)
			for offset := split; offset < len(message)-3; {
				end := min(offset+10*1024, len(message)-3)
				check(message[offset:end], false)
				offset = end
			}
			// Complete clipboard and begin a real, fragmented key in one frame.
			key := []byte{4, 1, 0, 0, 0, 0, 0, 'h'}
			check(append(append([]byte{}, message[len(message)-3:]...), key[:5]...), false)
			check(key[5:], true)
			if string(text) != "h" || callbacks != 1 {
				t.Fatalf("clipboard leaked: text=%q callbacks=%d", string(text), callbacks)
			}
		})
	}
}

func BenchmarkFragmentedClipboardPreview(b *testing.B) {
	for _, size := range []int{256 << 10, 512 << 10, 1 << 20} {
		for _, preview := range []bool{false, true} {
			b.Run(fmt.Sprintf("bytes=%d/preview=%v", size, preview), func(b *testing.B) {
				message := clipboardPreviewMessage(size)
				b.ReportAllocs()
				b.SetBytes(int64(size))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					p := Input{phase: 3}
					for offset := 0; offset < len(message); offset += 10 * 1024 {
						frame := message[offset:min(offset+10*1024, len(message))]
						if preview && p.AddsText(frame) {
							b.Fatal("clipboard classified as typing")
						}
						p.Feed(frame)
					}
				}
			})
		}
	}
}
