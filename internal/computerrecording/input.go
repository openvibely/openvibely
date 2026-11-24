package computerrecording

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Input observes the client side of the helper's RFB 3.8 / None connection.
// It never alters traffic or retains clipboard contents. Text requires an explicit
// field classification; absent detection is unknown, never implicitly ordinary.
// Unsupported messages disable recording instead of misinterpreting bytes.
type Input struct {
	buf         []byte
	phase       int
	disabled    bool
	buttons     byte
	Action      func(string)
	Text        func(rune, InputSecurity)
	Backspace   func()
	Security    func() InputSecurity
	modifiers   map[uint32]bool // Held modifiers; value is whether used in a combination.
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
				p.key(binary.BigEndian.Uint32(p.buf[4:8]), p.buf[1] != 0)
			case 255:
				p.key(binary.BigEndian.Uint32(p.buf[4:8]), binary.BigEndian.Uint16(p.buf[2:4]) != 0)
			case 5:
				mask := p.buf[1]
				x, y := binary.BigEndian.Uint16(p.buf[2:4]), binary.BigEndian.Uint16(p.buf[4:6])
				pressed := mask &^ p.buttons
				prefix := p.mouseModifierPrefix()
				if pressed != 0 {
					p.useModifiers()
				}
				for i, name := range []string{"Left", "Middle", "Right"} {
					if pressed&(1<<i) != 0 {
						p.Action(prefix + fmt.Sprintf("%s mouse down at %d, %d", name, x, y))
					}
					if p.buttons&^mask&(1<<i) != 0 {
						p.Action(prefix + fmt.Sprintf("%s mouse up at %d, %d", name, x, y))
					}
				}
				if pressed&8 != 0 {
					p.Action(prefix + "Scroll up")
				}
				if pressed&16 != 0 {
					p.Action(prefix + "Scroll down")
				}
				p.buttons = mask
			case 6:
				// Clipboard payload is deliberately excluded. Paste is a shortcut.
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

// InputSecurity is a positive classification of the focused input, not a guess.
type InputSecurity uint8

const (
	InputUnknown InputSecurity = iota
	InputOrdinary
	InputSecure
)

func (p *Input) typed(ch rune) {
	security := InputUnknown
	if p.Security != nil {
		security = p.Security()
	}
	if p.Text != nil {
		p.Text(ch, security)
		return
	}
	if security == InputSecure {
		p.Action("Type [secure input]")
	} else {
		p.Action("Type [input withheld: secure-input detection unavailable]")
	}
}

var modifierNames = map[uint32]string{0xffe1: "Shift", 0xffe2: "Shift", 0xffe3: "Ctrl", 0xffe4: "Ctrl", 0xffe9: "Alt", 0xffea: "Alt", 0xffe7: "Meta", 0xffe8: "Meta", 0xffeb: "Super", 0xffec: "Super", 0xfe03: "AltGr"}

// Mouse actions carry the modifiers held at that event, including releases.
// Collapse left/right variants and keep combination ordering deterministic.
func (p *Input) mouseModifierPrefix() string {
	held := map[string]bool{}
	for key := range p.modifiers {
		held[modifierNames[key]] = true
	}
	var names []string
	for _, name := range []string{"Ctrl", "Alt", "Meta", "Super", "Shift", "AltGr"} {
		if held[name] {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return strings.Join(names, "+") + "+"
}

func (p *Input) useModifiers() {
	for key := range p.modifiers {
		p.modifiers[key] = true
	}
}

func (p *Input) key(k uint32, down bool) {
	if name, ok := modifierNames[k]; ok {
		used, pressed := p.modifiers[k]
		if down && !pressed {
			combined := len(p.modifiers) > 0 || p.buttons&7 != 0
			p.useModifiers()
			if p.modifiers == nil {
				p.modifiers = map[uint32]bool{}
			}
			p.modifiers[k] = combined
		} else if !down && pressed {
			delete(p.modifiers, k)
			// Only a completed, unused tap is a standalone action. Repeated
			// keydowns and unmatched releases must not add extra actions.
			if !used {
				p.Action("Press " + name)
			}
		}
		return
	}
	if !down {
		return
	}
	p.useModifiers()
	held := map[string]bool{}
	for key := range p.modifiers {
		held[modifierNames[key]] = true
	}
	var prefix []string
	for _, name := range []string{"Ctrl", "Alt", "Meta", "Super"} {
		if held[name] {
			prefix = append(prefix, name)
		}
	}
	names := map[uint32]string{0xff0d: "Enter", 0xff09: "Tab", 0xff1b: "Escape", 0xff08: "Backspace", 0xffff: "Delete", 0xff51: "Left", 0xff52: "Up", 0xff53: "Right", 0xff54: "Down", 0xff50: "Home", 0xff57: "End", 0xff55: "PageUp", 0xff56: "PageDown"}
	name := names[k]
	if k >= 0xffbe && k <= 0xffc9 {
		name = fmt.Sprintf("F%d", k-0xffbe+1)
	}
	ch := rune(k)
	if k&0xff000000 == 0x01000000 {
		ch = rune(k & 0xffffff)
	} else if k > 0xff {
		ch = legacyKeysyms[k]
	}
	// noVNC sends printable keypad keys as distinct X11 keysyms. Decode
	// them before classification so they cannot bypass secure-input redaction.
	switch {
	case k >= 0xffb0 && k <= 0xffb9: // KP_0 through KP_9
		ch = '0' + rune(k-0xffb0)
	case k >= 0xffaa && k <= 0xffaf: // multiply, add, separator, subtract, decimal, divide
		ch = rune("*+,-./"[k-0xffaa])
	case k == 0xff80: // KP_Space
		ch = ' '
	case k == 0xffbd: // KP_Equal
		ch = '='
	}
	printable := utf8.ValidRune(ch) && ch >= 0x20 && !(ch >= 0x7f && ch <= 0x9f)
	if len(prefix) == 0 {
		if k == 0xff0d && held["Shift"] {
			p.typed('\n')
			return
		}
		if k == 0xff08 && !held["Shift"] && p.Backspace != nil {
			p.Backspace()
			return
		}
		if printable {
			p.typed(ch)
			return
		}
	}
	if held["Shift"] {
		prefix = append(prefix, "Shift")
	}
	if held["AltGr"] {
		prefix = append(prefix, "AltGr")
	}
	if name == "" {
		if printable {
			name = strconv.QuoteRune(ch)
		} else {
			name = fmt.Sprintf("keysym 0x%x", k)
		}
	}
	prefix = append(prefix, name)
	p.Action("Press " + strings.Join(prefix, "+"))
}
