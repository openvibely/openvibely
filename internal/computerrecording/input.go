package computerrecording

import (
	"encoding/binary"
	"fmt"
)

// Input observes the client side of the helper's RFB 3.8 / None connection.
// It never alters traffic and never retains clipboard or typed text.
// Unsupported messages disable recording instead of misinterpreting bytes.
type Input struct {
	buf         []byte
	phase       int
	disabled    bool
	buttons     byte
	Action      func(string)
	Unsupported func()
}

func (p *Input) Feed(b []byte) {
	if p.disabled {
		if p.Unsupported != nil {
			p.Unsupported()
		}
		return
	}
	if len(p.buf)+len(b) > 2<<20 {
		p.disable()
		return
	}
	p.buf = append(p.buf, b...)
	for len(p.buf) > 0 {
		n := 0
		switch p.phase {
		case 0:
			n = 12
			if len(p.buf) < n {
				return
			}
			if string(p.buf[:n]) != "RFB 003.008\n" {
				p.disable()
				return
			}
			p.phase++
		case 1:
			n = 1
			if p.buf[0] != 1 {
				p.disable()
				return
			}
			p.phase++
		case 2:
			n = 1
			p.phase++
		default:
			switch p.buf[0] {
			case 0:
				n = 20
			case 2:
				if len(p.buf) < 4 {
					return
				}
				n = 4 + 4*int(binary.BigEndian.Uint16(p.buf[2:4]))
			case 3:
				n = 10
			case 4:
				n = 8
			case 5:
				n = 6
			case 6:
				if len(p.buf) < 8 {
					return
				}
				size := int32(binary.BigEndian.Uint32(p.buf[4:8]))
				if size < 0 {
					size = -size
				}
				if size < 0 || size > 1<<20 {
					p.disable()
					return
				}
				n = 8 + int(size)
			case 248:
				if len(p.buf) < 9 {
					return
				}
				n = 9 + int(p.buf[8])
			case 150:
				n = 10
			case 251:
				if len(p.buf) < 8 {
					return
				}
				n = 8 + 16*int(p.buf[6])
			case 255:
				if len(p.buf) < 2 {
					return
				}
				if p.buf[1] != 0 {
					p.disable()
					return
				}
				n = 12
			default:
				p.disable()
				return
			}
			if len(p.buf) < n {
				return
			}
			switch p.buf[0] {
			case 4:
				if p.buf[1] != 0 {
					p.key(binary.BigEndian.Uint32(p.buf[4:8]))
				}
			case 255:
				if binary.BigEndian.Uint16(p.buf[2:4]) != 0 {
					p.key(binary.BigEndian.Uint32(p.buf[4:8]))
				}
			case 5:
				mask := p.buf[1]
				x, y := binary.BigEndian.Uint16(p.buf[2:4]), binary.BigEndian.Uint16(p.buf[4:6])
				pressed := mask &^ p.buttons
				for i, name := range []string{"Left", "Middle", "Right"} {
					if pressed&(1<<i) != 0 {
						p.Action(fmt.Sprintf("%s mouse down at %d, %d", name, x, y))
					}
					if p.buttons&^mask&(1<<i) != 0 {
						p.Action(fmt.Sprintf("%s mouse up at %d, %d", name, x, y))
					}
				}
				if pressed&8 != 0 {
					p.Action("Scroll up")
				}
				if pressed&16 != 0 {
					p.Action("Scroll down")
				}
				p.buttons = mask
			case 6:
				p.Action("Clipboard updated [user-provided value omitted]")
			}
		}
		if len(p.buf) < n {
			return
		}
		p.buf = p.buf[n:]
	}
	p.buf = nil
}
func (p *Input) disable() {
	p.disabled = true
	p.buf = nil
	if p.Unsupported != nil {
		p.Unsupported()
	}
}
func (p *Input) key(k uint32) {
	// X11/RFB keysyms F1 through F12 are contiguous.
	if k >= 0xffbe && k <= 0xffc9 {
		p.Action(fmt.Sprintf("Press F%d", k-0xffbe+1))
		return
	}
	names := map[uint32]string{0xff0d: "Enter", 0xff09: "Tab", 0xff1b: "Escape", 0xff08: "Backspace", 0xffff: "Delete", 0xff51: "Left", 0xff52: "Up", 0xff53: "Right", 0xff54: "Down", 0xffe1: "Shift", 0xffe2: "Shift", 0xffe3: "Control", 0xffe4: "Control", 0xffe9: "Alt", 0xffea: "Alt", 0xff50: "Home", 0xff57: "End", 0xff55: "PageUp", 0xff56: "PageDown"}
	if name, ok := names[k]; ok {
		p.Action("Press " + name)
	} else {
		p.Action("Type [user-provided value omitted]")
	}
}
