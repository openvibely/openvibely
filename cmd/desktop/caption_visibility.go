package main

import (
	"math"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// The watermark is read and updated only on the native UI thread, alongside
// the actual visibility change. Event callbacks and queued work can arrive out
// of order. The page origin also prevents a reload's sequence reset from
// blocking updates or accepting delayed events from the previous document.
type captionVisibility struct {
	page, sequence float64
}

func (v *captionVisibility) handle(event *application.CustomEvent, enqueue func(func()), apply func(bool)) {
	if event.Sender != "main" {
		return
	}
	data, ok := event.Data.(map[string]any)
	if !ok {
		return
	}
	hidden, ok := data["hidden"].(bool)
	if !ok {
		return
	}
	page, ok := data["page"].(float64)
	if !ok || page <= 0 || math.IsNaN(page) || math.IsInf(page, 0) {
		return
	}
	sequence, ok := data["sequence"].(float64)
	if !ok || sequence < 1 || sequence > 9007199254740991 || math.Trunc(sequence) != sequence {
		return
	}
	enqueue(func() {
		if page < v.page || (page == v.page && sequence <= v.sequence) {
			return
		}
		v.page, v.sequence = page, sequence
		apply(hidden)
	})
}
