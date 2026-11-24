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
