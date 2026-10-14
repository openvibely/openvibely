package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"testing"
)

func TestCaptionVisibilityRejectsStaleUpdates(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		v := &captionVisibility{}
		var queue []func()
		var applied []bool
		send := func(page, sequence float64, hidden bool) {
			v.handle(&application.CustomEvent{Sender: "main", Data: map[string]any{"page": page, "sequence": sequence, "hidden": hidden}}, func(f func()) { queue = append(queue, f) }, func(h bool) { applied = append(applied, h) })
		}
		// Simulate concurrent callbacks arriving in reverse order, then separately
		// simulate the UI queue executing the updates in reverse order.
		if reverse {
			send(100, 1, true)
			send(100, 2, false)
		} else {
			send(100, 2, false)
			send(100, 1, true)
		}
		if reverse {
			queue[1]()
			queue[0]()
		} else {
			queue[0]()
			queue[1]()
		}
		if len(applied) != 1 || applied[0] {
			t.Fatalf("stale hide applied: %v", applied)
		}
		queue = nil
		send(200, 1, true)   // New document resets its sequence.
		send(100, 99, false) // Delayed event from the old document.
		send(200, 1, false)  // Duplicate must not change state.
		for _, f := range queue {
			f()
		}
		if len(applied) != 2 || !applied[1] {
			t.Fatalf("reload ordering failed: %v", applied)
		}
	}
}
