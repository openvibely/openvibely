package streamdecode

import "strings"

// Text retains immutable views of parser-owned text for failure recovery.
// Consecutive updates from the same builder replace the old view, so successful
// streams do not maintain a second complete text buffer. Filtered fragments can
// be appended separately without changing their delivery order.
type Text struct {
	parts []string
	owner any
	start int
}

func (t *Text) View(owner any, full string, start int) {
	if t.owner == owner && len(t.parts) > 0 {
		t.parts[len(t.parts)-1] = full[t.start:]
	} else {
		t.parts = append(t.parts, full[start:])
		t.owner, t.start = owner, start
	}
}
func (t *Text) Append(text string) {
	if text == "" {
		return
	}
	t.parts = append(t.parts, text)
	t.owner = nil
}
func (t *Text) String() string { return strings.Join(t.parts, "") }

// Boundary prevents a later raw view from spanning filtered-out text.
func (t *Text) Boundary() { t.owner = nil }
