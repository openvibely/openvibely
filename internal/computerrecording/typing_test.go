package computerrecording

import (
	"encoding/binary"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func typingRecorder(t *testing.T, security InputSecurity, extended bool) (*Recorder, *Input, func(uint32, bool)) {
	t.Helper()
	r := &Recorder{}
	if err := r.Start("typing"); err != nil {
		t.Fatal(err)
	}
	g, _, _ := r.Ticket()
	r.Capture(g, 0, []byte{1}, nil)
	p := &Input{Action: r.Action, Text: r.Text, Backspace: r.Backspace, Security: func() InputSecurity { return security }}
	p.Feed(append([]byte("RFB 003.008\n"), 1, 1))
	send := func(k uint32, down bool) {
		b := make([]byte, 8)
		b[0] = 4
		if down {
			b[1] = 1
		}
		if extended {
			b = make([]byte, 12)
			b[0] = 255
			if down {
				b[3] = 1
			}
		}
		binary.BigEndian.PutUint32(b[4:8], k)
		p.Feed(b)
	}
	return r, p, send
}
func eventTexts(r *Recorder) []string {
	var texts []string
	for _, e := range r.Status().Events {
		texts = append(texts, e.Text)
	}
	return texts
}
func TestTypingFinishAndRetry(t *testing.T) {
	for _, extended := range []bool{false, true} {
		r, _, send := typingRecorder(t, InputOrdinary, extended)
		for _, ch := range "hello café世界🙂" {
			k := uint32(ch)
			if k > 255 {
				k |= 0x01000000
			}
			send(k, true)
			send(k, false)
		}
		want := []string{`Type "hello café世界🙂"`}
		if got := eventTexts(r); !reflect.DeepEqual(got, want) {
			t.Fatalf("%v", got)
		}
		// A failed finish must retain the unfinished text, as must pause and retries.
		if _, err := r.Finish("typing", 1); err == nil {
			t.Fatal("invalid budget accepted")
		}
		if extended {
			r.Pause()
		}
		for i := 0; i < 2; i++ {
			out, err := r.Finish("typing", 2)
			if err != nil || len(out.Events) != 1 || out.Events[0].Text != want[0] {
				t.Fatalf("%+v %v", out, err)
			}
		}
	}
}
func TestTypingBoundariesEditingAndShortcuts(t *testing.T) {
	r, p, send := typingRecorder(t, InputOrdinary, false)
	send('a', true)
	r.mu.Lock()
	r.lastTyped = time.Now().Add(-3 * time.Second)
	r.mu.Unlock()
	send('b', true)
	p.Feed([]byte{5, 1, 0, 10, 0, 20, 5, 0, 0, 10, 0, 20})
	send(0x0101f642, true)
	send(0xff08, true)
	send('c', true)
	send(0xffe1, true)
	send(0xff0d, true)
	send(0xffe1, false)
	send('d', true)
	for _, ch := range "cva" {
		send(0xffe3, true)
		send(uint32(ch), true)
		send(0xffe3, false)
	}
	send('e', true)
	send(0xff51, true)
	send(0xff51, true)
	send(0xff51, true)
	send(0xff08, true)
	want := []string{`Type "a"`, `Type "b"`, "Left mouse down at 10, 20", "Left mouse up at 10, 20", `Type "c\nd"`, "Press Ctrl+'c'", "Press Ctrl+'v'", "Press Ctrl+'a'", `Type "e"`, "Press Left", "Press Left", "Press Left", "Press Backspace"}
	if got := eventTexts(r); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
	// Clipboard traffic must not add a second paste event or its payload.
	b := append([]byte{6, 0, 0, 0, 0, 0, 0, 6}, []byte("secret")...)
	p.Feed(b)
	if got := eventTexts(r); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}
func TestTypingSecureAndUnknown(t *testing.T) {
	r, p, send := typingRecorder(t, InputSecure, false)
	for _, ch := range "password" {
		send(uint32(ch), true)
	}
	p.Security = nil
	for _, ch := range "unknown" {
		send(uint32(ch), true)
	}
	p.Security = func() InputSecurity { return InputOrdinary }
	send('x', true)
	want := []string{"Type [secure input]", "Type [input withheld: secure-input detection unavailable]", `Type "x"`}
	if got := eventTexts(r); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	if strings.Contains(r.Status().Transcript(), "password") {
		t.Fatal("secret retained")
	}
}
func TestTranscriptEscapesTypedStructure(t *testing.T) {
	r, _, _ := typingRecorder(t, InputOrdinary, false)
	for _, ch := range "\"\n[0.000s] Screenshot: fake\r\t\\\u2028\u202e" {
		r.Text(ch, InputOrdinary)
	}
	transcript := r.Status().Transcript()
	if strings.Count(transcript, "\n") != 2 || !strings.Contains(transcript, `\"\n[0.000s] Screenshot: fake\r\t\\\u2028\u202e`) || strings.Contains(transcript, "values omitted") {
		t.Fatal(transcript)
	}
}
func TestTypingConcurrentSnapshots(t *testing.T) {
	r, _, _ := typingRecorder(t, InputOrdinary, false)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			r.Text('l', InputOrdinary)
			r.Backspace()
			r.Action("Press Left")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			r.Status()
			r.Ticket()
		}
	}()
	wg.Wait()
	if len(r.Status().Events) != 200 {
		t.Fatal("repeated actions lost")
	}
}

func TestModifierSidesAndRelease(t *testing.T) {
	for _, extended := range []bool{false, true} {
		r, _, send := typingRecorder(t, InputOrdinary, extended)
		send(0xffe3, true)
		send(0xffe4, true)
		send(0xffe3, false)
		send('a', true)
		send(0xffe4, false)
		send('a', true)
		send(0xffe1, true)
		send(0xff51, true)
		send(0xffe1, false)
		send(0xff0d, true)
		want := []string{"Press Ctrl+'a'", `Type "a"`, "Press Shift+Left", "Press Enter"}
		if got := eventTexts(r); !reflect.DeepEqual(got, want) {
			t.Fatal(got)
		}
	}
}

func TestRightMouseEndsTypingAndLifecycleDoesNotLeakBuffer(t *testing.T) {
	r, p, send := typingRecorder(t, InputOrdinary, false)
	send('a', true)
	p.Feed([]byte{5, 4, 0, 0, 0, 0})
	send('b', true)
	want := []string{`Type "a"`, "Right mouse down at 0, 0", `Type "b"`}
	if got := eventTexts(r); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	r.Pause()
	send('x', true)
	if err := r.Start("typing"); err != nil {
		t.Fatal(err)
	}
	send('c', true)
	if got := eventTexts(r); got[len(got)-1] != `Type "c"` {
		t.Fatal(got)
	}
	r.Clear("typing")
	r.Start("new")
	send('d', true)
	if got := eventTexts(r); !reflect.DeepEqual(got, []string{`Type "d"`}) {
		t.Fatal(got)
	}
}

func TestTypingKeepsEventAndDurationSafeguards(t *testing.T) {
	r, _, _ := typingRecorder(t, InputOrdinary, false)
	for i := 0; i < 20000; i++ {
		r.Action("Press Left")
	}
	r.Text('a', InputOrdinary)
	if s := r.Status(); s.State != "stopped" || len(s.Events) != 20000 || s.Error == "" {
		t.Fatalf("%s %d %s", s.State, len(s.Events), s.Error)
	}
	r.Clear("typing")
	r.Start("expired")
	r.mu.Lock()
	r.result.Started = time.Now().Add(-Duration)
	r.mu.Unlock()
	r.Text('a', InputOrdinary)
	r.Backspace()
	if len(r.Status().Events) != 0 {
		t.Fatal("input after deadline")
	}
}

func TestViewerLegacyUnicodeKeysyms(t *testing.T) {
	r, _, send := typingRecorder(t, InputOrdinary, false)
	// noVNC uses legacy X11 codes for these characters, not Unicode keysyms.
	for _, k := range []uint32{0x01b1, 0x06c1, 0x07e1, 0x04b1} {
		send(k, true)
	}
	if got := eventTexts(r); !reflect.DeepEqual(got, []string{`Type "ąаαア"`}) {
		t.Fatal(got)
	}
}

func TestPrintableKeypadSecurity(t *testing.T) {
	keys := []uint32{0xffb1, 0xffb1, 0xffb2, 0xffb3, 0xffb4, 0xffb5, 0xffb6, 0xffb7, 0xffb8, 0xffb9, 0xffb0, 0xffaa, 0xffab, 0xffac, 0xffad, 0xffae, 0xffaf, 0xff80, 0xffbd}
	for _, extended := range []bool{false, true} {
		for _, tc := range []struct {
			name     string
			security InputSecurity
			want     string
		}{
			{"ordinary", InputOrdinary, `Type "11234567890*+,-./ ="`},
			{"secure", InputSecure, "Type [secure input]"},
			{"unknown", InputUnknown, "Type [input withheld: secure-input detection unavailable]"},
		} {
			t.Run(tc.name+map[bool]string{false: "/standard", true: "/extended"}[extended], func(t *testing.T) {
				r, p, send := typingRecorder(t, tc.security, extended)
				classified := 0
				p.Security = func() InputSecurity { classified++; return tc.security }
				for _, key := range keys {
					send(key, true)
					send(key, false)
				}
				if classified != len(keys) {
					t.Fatalf("classified %d of %d keypad characters", classified, len(keys))
				}
				if got := eventTexts(r); !reflect.DeepEqual(got, []string{tc.want}) {
					t.Fatalf("keypad events: %#v", got)
				}
				out, err := r.Finish("typing", 2)
				if err != nil {
					t.Fatal(err)
				}
				if len(out.Events) != 1 || out.Events[0].Text != tc.want || strings.Contains(out.Transcript(), "keysym") {
					t.Fatalf("saved keypad events: %#v", out.Events)
				}
			})
		}
	}
}

func TestKeypadShortcutRemainsAnAction(t *testing.T) {
	r, _, send := typingRecorder(t, InputOrdinary, false)
	send(0xffe3, true)
	send(0xffb1, true)
	send(0xffb1, false)
	send(0xffe3, false)
	send(0xffb2, true)
	if got := eventTexts(r); !reflect.DeepEqual(got, []string{"Press Ctrl+'1'", `Type "2"`}) {
		t.Fatal(got)
	}
}

func TestStandaloneModifierTaps(t *testing.T) {
	for _, extended := range []bool{false, true} {
		for _, tc := range []struct {
			key  uint32
			name string
		}{
			{0xffe1, "Shift"}, {0xffe2, "Shift"},
			{0xffe3, "Ctrl"}, {0xffe4, "Ctrl"},
			{0xffe9, "Alt"}, {0xffea, "Alt"},
			{0xffe7, "Meta"}, {0xffe8, "Meta"},
			{0xffeb, "Super"}, {0xffec, "Super"}, {0xfe03, "AltGr"},
		} {
			r, _, send := typingRecorder(t, InputOrdinary, extended)
			send('a', true)
			send(tc.key, false) // An unmatched release is not a tap.
			send(tc.key, true)
			send(tc.key, true) // Auto-repeat is still one tap.
			send(tc.key, false)
			send(tc.key, false)
			send('b', true)
			send(tc.key, true)
			send(tc.key, false) // A second real tap must be retained.
			want := []string{`Type "a"`, "Press " + tc.name, `Type "b"`, "Press " + tc.name}
			if got := eventTexts(r); !reflect.DeepEqual(got, want) {
				t.Fatalf("key=%x extended=%v: got %#v, want %#v", tc.key, extended, got, want)
			}
		}
	}
}

func TestUsedModifiersDoNotBecomeStandaloneTaps(t *testing.T) {
	for _, extended := range []bool{false, true} {
		r, _, send := typingRecorder(t, InputOrdinary, extended)
		send(0xffe3, true)
		send(0xffe9, true)
		send('c', true)
		send('c', false)
		send(0xffe3, true) // Repeated down must not reset the used state.
		send(0xffe9, false)
		send(0xffe3, false)
		send(0xffe1, true)
		send('A', true)
		send('A', false)
		send(0xffe1, false)
		send(0xfe03, true)
		send(0x010020ac, true)
		send(0x010020ac, false)
		send(0xfe03, false)
		want := []string{"Press Ctrl+Alt+'c'", `Type "A€"`}
		if got := eventTexts(r); !reflect.DeepEqual(got, want) {
			t.Fatalf("extended=%v: %#v", extended, got)
		}
	}
}

func TestMouseCombinationDoesNotBecomeModifierTap(t *testing.T) {
	for _, extended := range []bool{false, true} {
		r, p, send := typingRecorder(t, InputOrdinary, extended)
		send(0xffe1, true)
		p.Feed([]byte{5, 1, 0, 10, 0, 20, 5, 0, 0, 10, 0, 20})
		send(0xffe1, false)
		send(0xffe3, true)
		p.Feed([]byte{5, 8, 0, 10, 0, 20, 5, 0, 0, 10, 0, 20})
		send(0xffe3, false)
		want := []string{"Shift+Left mouse down at 10, 20", "Shift+Left mouse up at 10, 20", "Ctrl+Scroll up"}
		if got := eventTexts(r); !reflect.DeepEqual(got, want) {
			t.Fatalf("extended=%v: %#v", extended, got)
		}
	}
}

func TestMouseModifierReleaseAndRepeatedScroll(t *testing.T) {
	for _, extended := range []bool{false, true} {
		r, p, send := typingRecorder(t, InputOrdinary, extended)
		send(0xffe4, true) // Right Ctrl remains held after left Ctrl is released.
		send(0xffe3, true)
		send(0xffe2, true)
		p.Feed([]byte{5, 4, 0, 10, 0, 20})
		send(0xffe3, false)
		send(0xffe2, false)
		p.Feed([]byte{5, 0, 0, 10, 0, 20})
		for i := 0; i < 2; i++ {
			p.Feed([]byte{5, 16, 0, 10, 0, 20, 5, 0, 0, 10, 0, 20})
		}
		send(0xffe4, false)
		p.Feed([]byte{5, 2, 0, 10, 0, 20, 5, 0, 0, 10, 0, 20})
		p.Feed([]byte{5, 8, 0, 10, 0, 20, 5, 0, 0, 10, 0, 20})
		want := []string{
			"Ctrl+Shift+Right mouse down at 10, 20",
			"Ctrl+Right mouse up at 10, 20",
			"Ctrl+Scroll down", "Ctrl+Scroll down",
			"Middle mouse down at 10, 20", "Middle mouse up at 10, 20",
			"Scroll up",
		}
		if got := eventTexts(r); !reflect.DeepEqual(got, want) {
			t.Fatalf("extended=%v: got %#v, want %#v", extended, got, want)
		}
	}
}

func TestMouseActionsIncludeOtherModifiers(t *testing.T) {
	for _, extended := range []bool{false, true} {
		for _, tc := range []struct {
			key  uint32
			name string
		}{
			{0xffea, "Alt"}, {0xffe8, "Meta"}, {0xffec, "Super"}, {0xfe03, "AltGr"},
		} {
			r, p, send := typingRecorder(t, InputOrdinary, extended)
			send(tc.key, true)
			p.Feed([]byte{5, 1, 0, 10, 0, 20, 5, 0, 0, 10, 0, 20})
			send(tc.key, false)
			want := []string{tc.name + "+Left mouse down at 10, 20", tc.name + "+Left mouse up at 10, 20"}
			if got := eventTexts(r); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s extended=%v: %#v", tc.name, extended, got)
			}
		}
	}
}

func TestModifierOnlyCombinationRecordedOnce(t *testing.T) {
	for _, extended := range []bool{false, true} {
		for _, release := range [][]uint32{{0xffe9, 0xffe1}, {0xffe1, 0xffe9}} {
			r, _, send := typingRecorder(t, InputOrdinary, extended)
			send('a', true)
			send(0xffe9, true)
			send(0xffe1, true)
			send(0xffe1, true) // Repeated down is not another combination.
			for _, key := range release {
				send(key, false)
			}
			send(release[0], false) // Unmatched release is ignored.
			send('b', true)
			want := []string{`Type "a"`, "Press Alt+Shift", `Type "b"`}
			if got := eventTexts(r); !reflect.DeepEqual(got, want) {
				t.Fatalf("extended=%v release=%x: %#v", extended, release, got)
			}
		}
	}
}

func TestModifierCombinationConsumedByLargerAction(t *testing.T) {
	for _, extended := range []bool{false, true} {
		for _, mouse := range []bool{false, true} {
			r, p, send := typingRecorder(t, InputOrdinary, extended)
			send(0xffe9, true)
			send(0xffe1, true)
			want := []string{"Press Alt+Shift+Tab"}
			if mouse {
				p.Feed([]byte{5, 8, 0, 10, 0, 20, 5, 0, 0, 10, 0, 20})
				want = []string{"Alt+Shift+Scroll up"}
			} else {
				send(0xff09, true)
				send(0xff09, false)
			}
			send(0xffe1, false)
			send(0xffe9, false)
			if got := eventTexts(r); !reflect.DeepEqual(got, want) {
				t.Fatalf("extended=%v mouse=%v: %#v", extended, mouse, got)
			}
		}
	}
}

func TestModifierCombinationsWhileAnotherModifierStaysHeld(t *testing.T) {
	for _, extended := range []bool{false, true} {
		r, _, send := typingRecorder(t, InputOrdinary, extended)
		send(0xffe9, true)
		for i := 0; i < 2; i++ {
			send(0xffe1, true)
			send(0xffe1, false)
		}
		send(0xffe9, false)
		// A previously consumed Ctrl can participate in a fresh combination.
		send(0xffe3, true)
		send('c', true)
		send('c', false)
		send(0xffe1, true)
		send(0xffe3, false)
		send(0xffe1, false)
		want := []string{"Press Alt+Shift", "Press Alt+Shift", "Press Ctrl+'c'", "Press Ctrl+Shift"}
		if got := eventTexts(r); !reflect.DeepEqual(got, want) {
			t.Fatalf("extended=%v: %#v", extended, got)
		}
	}
}

func TestModifierCombinationCollapsesSidesAndHandlesThreeKeys(t *testing.T) {
	for _, extended := range []bool{false, true} {
		r, _, send := typingRecorder(t, InputOrdinary, extended)
		for _, key := range []uint32{0xffe4, 0xffe3, 0xffea, 0xffe2} {
			send(key, true)
		}
		for _, key := range []uint32{0xffe3, 0xffe2, 0xffe4, 0xffea} {
			send(key, false)
		}
		if got := eventTexts(r); !reflect.DeepEqual(got, []string{"Press Ctrl+Alt+Shift"}) {
			t.Fatalf("extended=%v: %#v", extended, got)
		}
	}
}

func TestShiftEnterVariantsUseTextSecurity(t *testing.T) {
	for _, extended := range []bool{false, true} {
		for _, enter := range []uint32{0xff0d, 0xff8d} {
			for _, tc := range []struct {
				security InputSecurity
				want     string
			}{
				{InputOrdinary, `Type "a\nb"`},
				{InputSecure, "Type [secure input]"},
				{InputUnknown, "Type [input withheld: secure-input detection unavailable]"},
			} {
				r, p, send := typingRecorder(t, tc.security, extended)
				classified := 0
				p.Security = func() InputSecurity { classified++; return tc.security }
				send('a', true)
				send('a', false)
				send(0xffe1, true)
				send(enter, true)
				send(enter, false)
				send(0xffe1, false)
				send('b', true)
				send('b', false)
				out, err := r.Finish("typing", 2)
				if err != nil {
					t.Fatal(err)
				}
				if classified != 3 || len(out.Events) != 1 || out.Events[0].Text != tc.want {
					t.Fatalf("extended=%v enter=%x security=%d: classified=%d events=%#v", extended, enter, tc.security, classified, out.Events)
				}
			}
		}
	}
}

func TestKeypadEnterControlActions(t *testing.T) {
	for _, extended := range []bool{false, true} {
		r, _, send := typingRecorder(t, InputOrdinary, extended)
		for i := 0; i < 2; i++ {
			send(0xff8d, true)
			send(0xff8d, false)
		}
		send(0xffe3, true)
		send(0xff8d, true)
		send(0xff8d, false)
		send(0xffe1, true)
		send(0xff8d, true)
		send(0xff8d, false)
		send(0xffe1, false)
		send(0xffe3, false)
		want := []string{"Press Enter", "Press Enter", "Press Ctrl+Enter", "Press Ctrl+Shift+Enter"}
		if got := eventTexts(r); !reflect.DeepEqual(got, want) {
			t.Fatalf("extended=%v: %#v", extended, got)
		}
	}
}
