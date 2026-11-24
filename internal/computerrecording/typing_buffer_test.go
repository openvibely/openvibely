package computerrecording

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

func BenchmarkTypingChunk(b *testing.B) {
	for _, size := range []int{5000, 10000, 20000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var r Recorder
				if err := r.Start("benchmark"); err != nil {
					b.Fatal(err)
				}
				for j := 0; j < size; j++ {
					r.Text('a', InputOrdinary)
				}
				r.Pause()
				if len(r.Status().Events) != 1 {
					b.Fatal("lost typing chunk")
				}
			}
		})
	}
}

func TestTypingSnapshotsKeepEditingAndPriorSnapshots(t *testing.T) {
	r, _, _ := typingRecorder(t, InputOrdinary, false)
	for _, ch := range "hello🙂" {
		r.Text(ch, InputOrdinary)
	}
	first := r.Status()
	r.Backspace()
	r.Text('!', InputOrdinary)
	second := r.Status()
	if first.Events[0].Text != `Type "hello🙂"` || second.Events[0].Text != `Type "hello!"` {
		t.Fatalf("snapshots: %v / %v", first.Events, second.Events)
	}
	r.Text('\n', InputOrdinary)
	r.Text('世', InputOrdinary)
	r.Action("Press Enter")
	out, err := r.Finish("typing", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Events) != 2 || out.Events[0].Text != `Type "hello!\n世"` || out.Events[1].Text != "Press Enter" {
		t.Fatal(out.Events)
	}
	if second.Events[0].Text != `Type "hello!"` {
		t.Fatal("snapshot changed after further typing")
	}
}

func TestBufferedTypingSurvivesLifecycleBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(*Recorder)
	}{
		{"pause", func(r *Recorder) { r.Pause() }},
		{"capture failure", func(r *Recorder) { g, _, _ := r.Ticket(); r.CaptureFailed(g, fmt.Errorf("capture failed")) }},
		{"fatal failure", func(r *Recorder) { r.Fail(fmt.Errorf("input failed")) }},
		{"duration", func(r *Recorder) { r.result.Started = time.Now().Add(-Duration); r.CaptureState() }},
		{"start retry", func(r *Recorder) {
			if err := r.Start("typing"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, _ := typingRecorder(t, InputOrdinary, false)
			for _, ch := range "hello世界" {
				r.Text(ch, InputOrdinary)
			}
			tc.stop(r)
			got := r.Status().Events
			if len(got) != 1 || got[0].Text != `Type "hello世界"` {
				t.Fatal(got)
			}
		})
	}
}

func TestTypingChunkAllocationsAreNotPerCharacter(t *testing.T) {
	const size = 20000
	allocs := testing.AllocsPerRun(1, func() {
		var r Recorder
		if err := r.Start("allocations"); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < size; i++ {
			r.Text('a', InputOrdinary)
		}
		r.Pause()
		events := r.Status().Events
		if len(events) != 1 || events[0].Text != "Type "+strconv.Quote(strings.Repeat("a", size)) {
			t.Fatal("long chunk changed")
		}
	})
	if allocs >= size {
		t.Fatalf("%.0f allocations for %d characters; typing still allocates per character", allocs, size)
	}
}
