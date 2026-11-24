package computerrecording

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestLongRecordingBoundsMemoryAndSelectsChronologically(t *testing.T) {
	r := &Recorder{}
	if err := r.Start("demo"); err != nil {
		t.Fatal(err)
	}
	g, _, _ := r.Ticket()
	for i := 0; i < 2400; i++ {
		r.Capture(g, int64(i*250), []byte{byte(i), byte(i >> 8)}, []byte{byte(i % 255)})
	}
	r.mu.Lock()
	if len(r.result.Frames) >= 720 {
		t.Fatal(len(r.result.Frames))
	}
	if r.interval <= 250*time.Millisecond {
		t.Fatal(r.interval)
	}
	r.result.Events = []Event{{1000, "Click reports"}, {200000, "Scroll down"}}
	r.mu.Unlock()
	for _, limit := range []int{2, 5, 30} {
		out, err := r.Finish("demo", limit)
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Frames) > limit || out.Frames[0].Millis != 0 || out.Frames[len(out.Frames)-1].Millis != 599750 {
			t.Fatalf("selection: %+v", out)
		}
		for i := 1; i < len(out.Frames); i++ {
			if out.Frames[i].Millis <= out.Frames[i-1].Millis {
				t.Fatal("unordered")
			}
		}
		if len(out.Events) != 2 {
			t.Fatal("actions discarded")
		}
	}
}
func TestPauseRejectsInFlightCaptureAndPreservesRetry(t *testing.T) {
	r := &Recorder{}
	r.Start("demo")
	g, ms, _ := r.Ticket()
	r.Capture(g, ms, []byte{1}, nil)
	r.Pause()
	r.Capture(g, ms+1, []byte{2}, nil)
	r.Action("secret")
	out, err := r.Finish("demo", 2)
	if err != nil || len(out.Frames) != 1 || len(out.Events) != 0 {
		t.Fatalf("%+v %v", out, err)
	}
	again, err := r.Finish("demo", 2)
	if err != nil || len(again.Frames) != 1 {
		t.Fatal("retry lost frames")
	}
	if err = r.Clear("other"); err == nil {
		t.Fatal("wrong ID cleared")
	}
	r.Clear("demo")
	if r.Status().ID != "" {
		t.Fatal("not cleared")
	}
}
func TestInputHandlesFragmentedCoalescedMessagesWithUnknownInput(t *testing.T) {
	var got []string
	p := Input{Action: func(s string) { got = append(got, s) }, Unsupported: func() { t.Fatal("unsupported") }}
	data := append([]byte("RFB 003.008\n"), 1, 1)
	key := make([]byte, 8)
	key[0] = 4
	key[1] = 1
	binary.BigEndian.PutUint32(key[4:], uint32('s'))
	data = append(data, key...)
	data = append(data, 5, 1, 0, 10, 0, 20, 5, 0, 0, 10, 0, 20)
	for _, b := range data {
		p.Feed([]byte{b})
	}
	if len(got) != 3 || got[0] != "Type [input withheld: secure-input detection unavailable]" || got[1] != "Left mouse down at 10, 20" {
		t.Fatal(got)
	}
}
func TestInputUnknownMessageFailsClosed(t *testing.T) {
	stopped := false
	p := Input{Action: func(string) { t.Fatal("unexpected action") }, Unsupported: func() { stopped = true }}
	p.Feed(append([]byte("RFB 003.008\n"), 1, 1, 234))
	if !stopped {
		t.Fatal("unknown protocol not stopped")
	}
}

func TestUnsupportedConnectionStillRejectsLaterRecording(t *testing.T) {
	failures := 0
	input := Input{Unsupported: func() { failures++ }}
	input.Feed([]byte("not RFB 3.8!"))
	input.Feed([]byte{1})
	if failures != 2 {
		t.Fatalf("unsupported connection silently accepted: %d", failures)
	}
}

// A menu can open and close before the first old sampling boundary. Its
// screenshot must still reach the model, even with only one interior slot.
func TestSelectionIncludesBriefActionsAcrossEntireRecording(t *testing.T) {
	for _, actionMS := range []int64{1000, 5000, 9000, 10000} {
		t.Run(time.Duration(actionMS*time.Millisecond.Nanoseconds()).String(), func(t *testing.T) {
			var r Recorder
			if err := r.Start("demo"); err != nil {
				t.Fatal(err)
			}
			generation, _, _ := r.Ticket()
			// A background change precedes the action; prefer the action frame.
			for _, ms := range []int64{0, 250, 1000, 5000, 9000, 10000, 10001} {
				value := byte(0)
				if ms == 250 {
					value = 20
				}
				if ms == actionMS {
					value = 255
				}
				r.Capture(generation, ms, []byte{value}, []byte{value})
			}
			r.result.Events = []Event{{actionMS - 1, "Open menu"}}
			out, err := r.Finish("demo", 3)
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Frames) != 3 {
				t.Fatalf("got %d frames, want first, action, last", len(out.Frames))
			}
			if out.Frames[0].Millis != 0 || out.Frames[1].Millis != actionMS || out.Frames[2].Millis != 10001 {
				t.Fatalf("selected times: %d, %d, %d; action at %d", out.Frames[0].Millis, out.Frames[1].Millis, out.Frames[2].Millis, actionMS)
			}
		})
	}
}

func TestCaptureFailureRecovery(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "save_existing", true: "resume_then_save"}[resume], func(t *testing.T) {
			var r Recorder
			r.Start("demo")
			g, _, _ := r.Ticket()
			r.Capture(g, 0, []byte{1}, nil)
			r.CaptureFailed(g, errors.New("capture timeout"))
			if s := r.Status(); s.State != "stopped" || s.Error == "" {
				t.Fatalf("failure not reported: %+v", s)
			}
			if resume {
				if err := r.Start("demo"); err != nil {
					t.Fatal(err)
				}
				if r.Status().Error == "" {
					t.Fatal("cleared error before successful capture")
				}
				g, _, _ = r.Ticket()
				r.Capture(g, 1000, []byte{2}, nil)
				if r.Status().Error != "" {
					t.Fatal("successful capture retained error")
				}
			}
			out, err := r.Finish("demo", 3)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if resume {
				want = 2
			}
			if len(out.Frames) != want {
				t.Fatalf("lost frames: %d", len(out.Frames))
			}
			retry, err := r.Finish("demo", 3)
			if err != nil || len(retry.Frames) != want {
				t.Fatalf("save retry: %+v %v", retry, err)
			}
		})
	}
}

func TestCaptureFailureWithoutFramesCanResumeAfterSaveAttempt(t *testing.T) {
	var r Recorder
	r.Start("demo")
	g, _, _ := r.Ticket()
	r.CaptureFailed(g, errors.New("timeout"))
	if _, err := r.Finish("demo", 3); err == nil {
		t.Fatal("saved empty recording")
	}
	if err := r.Start("demo"); err != nil {
		t.Fatal(err)
	}
	g, _, _ = r.Ticket()
	r.Capture(g, 1000, []byte{1}, nil)
	if _, err := r.Finish("demo", 3); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureRecoveryDoesNotEraseFatalOrNewerState(t *testing.T) {
	var r Recorder
	r.Start("demo")
	g, _, _ := r.Ticket()
	r.Capture(g, 0, []byte{1}, nil)
	r.Fail(errors.New("unsupported input protocol"))
	r.CaptureFailed(g, errors.New("late timeout"))
	r.Capture(g, 1000, []byte{2}, nil)
	if err := r.Start("demo"); err == nil {
		t.Fatal("resumed fatal error")
	}
	if _, err := r.Finish("demo", 3); err == nil {
		t.Fatal("saved incomplete action log")
	}
	r.Clear("demo")
	r.Start("new")
	r.CaptureFailed(g, errors.New("old timeout"))
	if s := r.Status(); s.State != "recording" || s.Error != "" {
		t.Fatalf("old failure poisoned new recording: %+v", s)
	}
}

func TestInputRecordsFunctionKeysAndRedactsUnknownText(t *testing.T) {
	for _, extended := range []bool{false, true} {
		t.Run(fmt.Sprintf("extended=%v", extended), func(t *testing.T) {
			var got []string
			p := Input{Action: func(s string) { got = append(got, s) }, Unsupported: func() { t.Fatal("unsupported input") }}
			p.Feed(append([]byte("RFB 003.008\n"), 1, 1))
			send := func(key uint32, down bool) {
				packet := make([]byte, 8)
				packet[0] = 4
				if down {
					packet[1] = 1
				}
				if extended {
					packet = make([]byte, 12)
					packet[0] = 255
					if down {
						binary.BigEndian.PutUint16(packet[2:4], 1)
					}
				}
				binary.BigEndian.PutUint32(packet[4:8], key)
				p.Feed(packet)
			}
			for key := uint32(0xffbe); key <= 0xffc9; key++ {
				send(key, true)
				send(key, false)
			}
			send(uint32('s'), true)
			send(uint32('s'), false)
			if len(got) != 13 {
				t.Fatalf("actions=%v", got)
			}
			for i := 0; i < 12; i++ {
				if want := fmt.Sprintf("Press F%d", i+1); got[i] != want {
					t.Errorf("got %q, want %q", got[i], want)
				}
			}
			if got[12] != "Type [input withheld: secure-input detection unavailable]" {
				t.Fatalf("typed text not redacted: %q", got[12])
			}
		})
	}
}

func TestCaptureStateAppliesLifecycleWithoutSnapshottingEvents(t *testing.T) {
	var r Recorder
	if err := r.Start("demo"); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.interval = 500 * time.Millisecond
	r.result.Events = []Event{{Millis: 10, Text: "preserve me"}}
	r.mu.Unlock()

	state, interval := r.CaptureState()
	if state != "recording" || interval != 500*time.Millisecond {
		t.Fatalf("state=%q interval=%s", state, interval)
	}

	r.mu.Lock()
	r.result.Started = time.Now().Add(-Duration)
	generation := r.generation
	r.mu.Unlock()
	state, interval = r.CaptureState()
	if state != "stopped" || interval != 500*time.Millisecond {
		t.Fatalf("ten-minute boundary: state=%q interval=%s", state, interval)
	}
	if r.generation != generation+1 {
		t.Fatalf("generation=%d, want %d", r.generation, generation+1)
	}
	if got := r.Status().Events; len(got) != 1 || got[0].Text != "preserve me" {
		t.Fatalf("stopping discarded action history: %+v", got)
	}
}

func TestCaptureStateExpiresAbandonedStoppedRecording(t *testing.T) {
	var r Recorder
	if err := r.Start("demo"); err != nil {
		t.Fatal(err)
	}
	r.Pause()
	r.mu.Lock()
	r.result.Started = time.Now().Add(-time.Hour - time.Second)
	r.result.Events = []Event{{Millis: 1, Text: "sensitive action"}}
	generation := r.generation
	r.mu.Unlock()

	state, interval := r.CaptureState()
	if state != "" {
		t.Fatalf("expired state=%q", state)
	}
	if interval != 250*time.Millisecond {
		t.Fatalf("interval changed during expiry: %s", interval)
	}
	if got := r.Status(); got.ID != "" || len(got.Events) != 0 {
		t.Fatalf("abandoned recording was retained: %+v", got)
	}
	if r.generation != generation+1 {
		t.Fatalf("generation=%d, want %d", r.generation, generation+1)
	}
}

func TestTicketAppliesLifecycleWithoutStatusSnapshot(t *testing.T) {
	var r Recorder
	if err := r.Start("demo"); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.result.Events = []Event{{Millis: 1, Text: "keep"}}
	r.result.Started = time.Now().Add(-Duration)
	generation := r.generation
	r.mu.Unlock()

	gotGeneration, _, active := r.Ticket()
	if active {
		t.Fatal("ticket remained active beyond the ten-minute limit")
	}
	if gotGeneration != generation+1 {
		t.Fatalf("generation=%d, want %d", gotGeneration, generation+1)
	}
	if got := r.Status().Events; len(got) != 1 || got[0].Text != "keep" {
		t.Fatalf("ticket read discarded action history: %+v", got)
	}
}

func TestStatusEventSnapshotRemainsIndependent(t *testing.T) {
	var r Recorder
	if err := r.Start("demo"); err != nil {
		t.Fatal(err)
	}
	r.Action("original")
	snapshot := r.Status()
	snapshot.Events[0].Text = "mutated snapshot"
	if got := r.Status().Events[0].Text; got != "original" {
		t.Fatalf("snapshot mutation reached recorder: %q", got)
	}
}

func TestConcurrentRecordingStateReadsAndActions(t *testing.T) {
	var r Recorder
	if err := r.Start("demo"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				switch worker {
				case 0:
					r.Action(fmt.Sprintf("action-%d", j))
				case 1:
					r.Ticket()
				case 2:
					r.Status()
				case 3:
					r.CaptureState()
				}
			}
		}(i)
	}
	wg.Wait()
}

func benchmarkRecorder(eventCount int, state string) *Recorder {
	r := &Recorder{
		result: Result{
			ID:      "benchmark",
			State:   state,
			Started: time.Now(),
			Events:  make([]Event, eventCount),
		},
		interval: 250 * time.Millisecond,
	}
	return r
}

func BenchmarkCaptureState(b *testing.B) {
	for _, state := range []string{"recording", "stopped"} {
		for _, eventCount := range []int{0, 1000, 10000, 20000} {
			b.Run(fmt.Sprintf("%s/events=%d", state, eventCount), func(b *testing.B) {
				r := benchmarkRecorder(eventCount, state)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					r.CaptureState()
				}
			})
		}
	}
}

func BenchmarkTicket(b *testing.B) {
	for _, state := range []string{"recording", "stopped"} {
		for _, eventCount := range []int{0, 1000, 10000, 20000} {
			b.Run(fmt.Sprintf("%s/events=%d", state, eventCount), func(b *testing.B) {
				r := benchmarkRecorder(eventCount, state)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					r.Ticket()
				}
			})
		}
	}
}

func TestRepeatedNavigationKeysWithinWindowAreAllRecorded(t *testing.T) {
	var r Recorder
	if err := r.Start("audit"); err != nil {
		t.Fatal(err)
	}
	p := Input{Action: r.Action, Unsupported: func() { t.Fatal("unsupported input") }}
	p.Feed(append([]byte("RFB 003.008\n"), 1, 1))
	send := func(key uint32, down bool) {
		packet := make([]byte, 8)
		packet[0] = 4
		if down {
			packet[1] = 1
		}
		binary.BigEndian.PutUint32(packet[4:8], key)
		p.Feed(packet)
	}
	for i := 0; i < 3; i++ {
		send(0xff54, true)
		send(0xff54, false)
	}
	events := r.Status().Events
	if len(events) != 3 {
		t.Fatalf("saved events=%d, want 3: %v", len(events), events)
	}
	for _, e := range events {
		if e.Text != "Press Down" {
			t.Fatalf("event text=%q, want Press Down", e.Text)
		}
	}
}
