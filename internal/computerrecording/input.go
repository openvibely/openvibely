package computerrecording

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Input observes the client side of the helper's RFB 3.8 / None connection.
// It never alters traffic or retains clipboard contents. Identified secure text
// is redacted; unavailable detection remains explicitly InputUnknown.
// Unsupported messages disable recording instead of misinterpreting bytes.
type Input struct {
	buf            []byte
	phase          int
	disabled       bool
	buttons        byte
	Action         func(string)
	ContinueAction func(string) // Navigation and pointer actions that do not end typing.
	Text           func(rune, InputSecurity)
	Backspace      func()
	Security       func() InputSecurity
	modifiers      map[uint32]bool // Held modifiers; value is whether already consumed by an action.
	Unsupported    func()
}

// AddsText previews a frame with the same decoder and modifier state as Feed.
// It neither consumes input nor invokes recording callbacks. This also handles
// fragmented keys and coalesced shortcuts without guessing from raw bytes.
func (p *Input) AddsText(b []byte) bool {
	if p.disabled || len(p.buf)+len(b) > 2<<20 {
		return false
	}
	preview := *p
	preview.buf = nil
	preview.modifiers = make(map[uint32]bool, len(p.modifiers))
	for key, used := range p.modifiers {
		preview.modifiers[key] = used
	}
	addsText := false
	preview.Action = func(string) {}
	preview.ContinueAction = func(string) {}
	preview.Backspace = func() {}
	preview.Unsupported = nil
	preview.Security = nil
	preview.Text = func(rune, InputSecurity) { addsText = true }
	preview.consume(inputView{first: p.buf, second: b})
	return addsText
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
	consumed := p.consume(inputView{first: p.buf})
	if p.disabled {
		return
	}
	p.buf = p.buf[consumed:]
	if len(p.buf) == 0 {
		p.buf = nil
	}
}

// Decode a read-only view shared by Feed and AddsText. Only message headers
// are inspected; clipboard and other ignored payloads are skipped by length.
func (p *Input) consume(data inputView) (consumed int) {
	var scratch [20]byte
	for data.len() > 0 {
		header := data.prefix(min(data.len(), len(scratch)), &scratch)
		n := 0
		switch p.phase {
		case 0:
			n = 12
			if data.len() < n {
				return
			}
			if string(header[:n]) != "RFB 003.008\n" {
				p.disable()
				return
			}
			p.phase++
		case 1:
			n = 1
			if header[0] != 1 {
				p.disable()
				return
			}
			p.phase++
		case 2:
			n = 1
			p.phase++
		default:
			switch header[0] {
			case 0:
				n = 20
			case 2:
				if data.len() < 4 {
					return
				}
				n = 4 + 4*int(binary.BigEndian.Uint16(header[2:4]))
			case 3:
				n = 10
			case 4:
				n = 8
			case 5:
				n = 6
			case 6:
				if data.len() < 8 {
					return
				}
				size := int32(binary.BigEndian.Uint32(header[4:8]))
				if size < 0 {
					size = -size
				}
				if size < 0 || size > 1<<20 {
					p.disable()
					return
				}
				n = 8 + int(size)
			case 248:
				if data.len() < 9 {
					return
				}
				n = 9 + int(header[8])
			case 150:
				n = 10
			case 251:
				if data.len() < 8 {
					return
				}
				n = 8 + 16*int(header[6])
			case 255:
				if data.len() < 2 {
					return
				}
				if header[1] != 0 {
					p.disable()
					return
				}
				n = 12
			default:
				p.disable()
				return
			}
			if data.len() < n {
				return
			}
			switch header[0] {
			case 4:
				p.key(binary.BigEndian.Uint32(header[4:8]), header[1] != 0)
			case 255:
				p.key(binary.BigEndian.Uint32(header[4:8]), binary.BigEndian.Uint16(header[2:4]) != 0)
			case 5:
				mask := header[1]
				x, y := binary.BigEndian.Uint16(header[2:4]), binary.BigEndian.Uint16(header[4:6])
				pressed := mask &^ p.buttons
				prefix := p.mouseModifierPrefix()
				if pressed != 0 {
					p.useModifiers()
				}
				for i, name := range []string{"Left", "Middle", "Right"} {
					if pressed&(1<<i) != 0 {
						if i == 1 && prefix == "" {
							p.continueAction(fmt.Sprintf("%s mouse down at %d, %d", name, x, y))
						} else {
							p.Action(prefix + fmt.Sprintf("%s mouse down at %d, %d", name, x, y))
						}
					}
					if p.buttons&^mask&(1<<i) != 0 {
						p.continueAction(prefix + fmt.Sprintf("%s mouse up at %d, %d", name, x, y))
					}
				}
				if pressed&8 != 0 {
					p.pointerAction(prefix, "Scroll up")
				}
				if pressed&16 != 0 {
					p.pointerAction(prefix, "Scroll down")
				}
				p.buttons = mask
			case 6:
				// Clipboard payload is deliberately excluded. Paste is a shortcut.
			}
		}
		if data.len() < n {
			return
		}
		data.skip(n)
		consumed += n
	}
	return
}
func (p *Input) disable() {
	p.disabled = true
	p.buf = nil
	if p.Unsupported != nil {
		p.Unsupported()
	}
}

// InputSecurity describes detection, independently of capture policy. Unknown
// input is captured; only positively identified secure input is redacted.
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
		p.Action("Type " + strconv.Quote(sanitizeText(string(ch))))
	}
}

// noVNC maps macOS left Option to Mode_switch and right Option to
// ISO_Level3_Shift. Both have AltGr semantics on the remote desktop.
var modifierNames = map[uint32]string{0xffe1: "Shift", 0xffe2: "Shift", 0xffe3: "Ctrl", 0xffe4: "Ctrl", 0xffe9: "Alt", 0xffea: "Alt", 0xffe7: "Meta", 0xffe8: "Meta", 0xffeb: "Super", 0xffec: "Super", 0xfe03: "AltGr", 0xff7e: "AltGr"}

// Collapse left/right variants and keep combination ordering deterministic.
func (p *Input) heldModifierNames() []string {
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
	return names
}

// Mouse actions carry the modifiers held at that event, including releases.
func (p *Input) mouseModifierPrefix() string {
	names := p.heldModifierNames()
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
	if _, ok := modifierNames[k]; ok {
		_, pressed := p.modifiers[k]
		if down && !pressed {
			if p.modifiers == nil {
				p.modifiers = map[uint32]bool{}
			}
			p.modifiers[k] = p.buttons&7 != 0
		} else if !down && pressed {
			names := p.heldModifierNames()
			unused := false
			for _, consumed := range p.modifiers {
				unused = unused || !consumed
			}
			// Complete an unused chord on its first release. Consume the
			// remaining modifiers so their releases cannot duplicate it.
			// Two sides of the same modifier are one logical key: wait for
			// its final release, since the other side can still form a shortcut.
			if unused && (len(names) > 1 || len(p.modifiers) == 1) {
				p.Action("Press " + strings.Join(names, "+"))
				p.useModifiers()
			}
			delete(p.modifiers, k)
		}
		return
	}
	if !down {
		return
	}
	// Normalize non-printable keypad keys before naming and shortcut handling.
	switch {
	case k == 0xff8d: // KP_Enter
		k = 0xff0d
	case k >= 0xff95 && k <= 0xff9d: // KP_Home through KP_Begin
		k -= 0x45
	case k == 0xff9e: // KP_Insert
		k = 0xff63
	case k == 0xff9f: // KP_Delete
		k = 0xffff
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
	names := map[uint32]string{0xff0d: "Enter", 0xff09: "Tab", 0xff1b: "Escape", 0xff08: "Backspace", 0xffff: "Delete", 0xff51: "Left", 0xff52: "Up", 0xff53: "Right", 0xff54: "Down", 0xff50: "Home", 0xff57: "End", 0xff55: "PageUp", 0xff56: "PageDown", 0xff58: "Begin", 0xff63: "Insert"}
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
	if len(prefix) == 0 {
		p.continueAction("Press " + name)
		return
	}
	prefix = append(prefix, name)
	p.Action("Press " + strings.Join(prefix, "+"))
}

func (p *Input) continueAction(text string) {
	if p.ContinueAction != nil {
		p.ContinueAction(text)
	} else {
		p.Action(text)
	}
}
func (p *Input) pointerAction(prefix, text string) {
	if prefix != "" {
		p.Action(prefix + text)
	} else {
		p.continueAction(text)
	}
}

// inputView joins buffered and incoming bytes without copying their payloads.
// Headers crossing the boundary use a small fixed scratch buffer.
type inputView struct{ first, second []byte }

func (v inputView) len() int { return len(v.first) + len(v.second) }
func (v inputView) prefix(n int, scratch *[20]byte) []byte {
	if len(v.first) >= n {
		return v.first[:n]
	}
	if len(v.first) == 0 {
		return v.second[:n]
	}
	copied := copy(scratch[:n], v.first)
	copy(scratch[copied:n], v.second)
	return scratch[:n]
}
func (v *inputView) skip(n int) {
	if n < len(v.first) {
		v.first = v.first[n:]
		return
	}
	n -= len(v.first)
	v.first = v.second[n:]
	v.second = nil
}
