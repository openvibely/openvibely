package contracts

import "time"

// CompactionProgress describes activity only; summaries never enter the display stream.
type CompactionProgress struct {
	State    string
	Duration time.Duration
}

// BeginCompaction reports a real attempt and returns its terminal callback.
func BeginCompaction(report func(CompactionProgress)) func(bool) {
	started := time.Now()
	if report != nil {
		report(CompactionProgress{State: "started"})
	}
	return func(success bool) {
		state := "failed"
		if success {
			state = "done"
		}
		if report != nil {
			report(CompactionProgress{State: state, Duration: time.Since(started)})
		}
	}
}
