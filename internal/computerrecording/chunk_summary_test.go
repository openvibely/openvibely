package computerrecording

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSpecialKeysRefreshTypingInactivity(t *testing.T) {
	for _, extended := range []bool{false, true} {
		for _, key := range []uint32{0xffbe, 0xff0d, 0xff1b, 0xffff, 0xff63} {
			r, _, send := typingRecorder(t, InputOrdinary, extended)
			send('a', true)
			r.lastTyped = time.Now().Add(-1500 * time.Millisecond)
			before := time.Now()
			send(key, true)
			send(key, false)
			if r.lastTyped.Before(before) {
				t.Fatalf("%x did not refresh inactivity", key)
			}
			r.lastTyped = r.lastTyped.Add(-1500 * time.Millisecond)
			send('b', true)
			got := eventTexts(r)
			if len(got) != 2 || got[0] != `Type "ab"` {
				t.Fatalf("%x: %v", key, got)
			}
		}
	}
}

func TestSpecialKeysSummarizedAcrossChunk(t *testing.T) {
	for _, extended := range []bool{false, true} {
		r, _, send := typingRecorder(t, InputOrdinary, extended)
		for _, key := range []uint32{0xff54, 0xff52, 0xff54, 0xffbe, 0xff0d, 0xffbe, 0xff0d} {
			send(key, true)
			send(key, false)
		}
		raw := r.Status().Events
		if len(raw) != 7 {
			t.Fatal(raw)
		}
		out, err := r.Finish("typing", 2)
		if err != nil {
			t.Fatal(err)
		}
		transcript := out.Transcript()
		want := []string{"Press Down ×2", "Press Up", "Press F1 ×2", "Press Enter ×2"}
		last := -1
		for _, text := range want {
			at := strings.Index(transcript, text)
			if at <= last {
				t.Fatal(transcript)
			}
			last = at
		}
		if strings.Count(transcript, "Press ") != 4 {
			t.Fatal(transcript)
		}
		// The helper serializes this snapshot before the app formats the transcript.
		data, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		var saved Result
		if err = json.Unmarshal(data, &saved); err != nil {
			t.Fatal(err)
		}
		if saved.Transcript() != transcript || !reflect.DeepEqual(saved.Events, raw) {
			t.Fatal("roundtrip changed chunks")
		}
		retry, err := r.Finish("typing", 2)
		if err != nil || retry.Transcript() != transcript {
			t.Fatal("retry changed chunks", err)
		}
	}
}

func TestSpecialKeySummaryBoundaries(t *testing.T) {
	for _, boundary := range []string{"gap", "shortcut", "left click", "pause", "security"} {
		r, p, send := typingRecorder(t, InputOrdinary, false)
		send('a', true)
		send(0xff54, true)
		send(0xff52, true)
		switch boundary {
		case "gap":
			r.lastTyped = time.Now().Add(-3 * time.Second)
		case "shortcut":
			send(0xffe3, true)
			send('c', true)
			send(0xffe3, false)
		case "left click":
			p.Feed([]byte{5, 1, 0, 10, 0, 20})
		case "pause":
			r.Pause()
			if err := r.Start("typing"); err != nil {
				t.Fatal(err)
			}
		case "security":
			r.Text('x', InputSecure)
		}
		send(0xff54, true)
		transcript := r.Status().Transcript()
		if strings.Contains(transcript, "Down ×2") || strings.Count(transcript, "Press Down") != 2 {
			t.Fatalf("%s: %s", boundary, transcript)
		}
	}
}

func TestSpecialKeySummaryIncludesInterveningTypingAndScroll(t *testing.T) {
	r, p, send := typingRecorder(t, InputOrdinary, false)
	send(0xff54, true)
	send('a', true)
	send(0xff52, true)
	p.Feed([]byte{5, 8, 0, 10, 0, 20, 5, 0, 0, 10, 0, 20})
	send('b', true)
	send(0xff54, true)
	out := r.Status()
	if !strings.Contains(out.Transcript(), "Press Down ×2") || !strings.Contains(out.Transcript(), `Type "ab"`) {
		t.Fatal(out.Transcript())
	}
}

func TestRepeatedShortcutsHaveSeparateTranscriptLines(t *testing.T) {
	for _, extended := range []bool{false, true} {
		r, _, send := typingRecorder(t, InputOrdinary, extended)
		send(0xffe3, true)
		for i := 0; i < 2; i++ {
			send(0xff54, true)
			send(0xff54, false)
		}
		send(0xffe3, false)
		transcript := r.Status().Transcript()
		var shortcutLines int
		for _, line := range strings.Split(transcript, "\n") {
			if strings.Contains(line, "Press Ctrl+Down") {
				shortcutLines++
			}
		}
		if shortcutLines != 2 || strings.Contains(transcript, "×") {
			t.Fatal(transcript)
		}
	}
}

func TestTextAndSpecialKeysShareTranscriptLine(t *testing.T) {
	for _, extended := range []bool{false, true} {
		for _, keysFirst := range []bool{false, true} {
			r, _, send := typingRecorder(t, InputOrdinary, extended)
			if !keysFirst {
				send('a', true)
			}
			send(0xff54, true)
			send(0xff52, true)
			send(0xff54, true)
			if keysFirst {
				send('a', true)
			}
			send('b', true)
			out, err := r.Finish("typing", 2)
			if err != nil {
				t.Fatal(err)
			}
			expected := `Type "ab"; Press Down ×2; Press Up`
			transcript := out.Transcript()
			if !strings.Contains(transcript, expected) {
				t.Fatal(transcript)
			}
			// Raw input remains available, and transport/retry preserve the joined line.
			if len(out.Events) != 4 {
				t.Fatal(out.Events)
			}
			data, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			var saved Result
			if err = json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			retry, err := r.Finish("typing", 2)
			if err != nil || saved.Transcript() != transcript || retry.Transcript() != transcript {
				t.Fatal("changed during save/retry", err)
			}
		}
	}
}
