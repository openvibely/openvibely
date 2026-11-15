package handler

import "time"

const (
	liveReconcileInitialDelay = time.Second
	liveReconcileMaxDelay     = 15 * time.Second
)

// liveReconcileSchedule belongs to the socket loop, not individual subscriptions.
// It retains one pending recovery during a cooldown. The existing connection
// tick checks deadlines; this adds no timer, goroutine, or periodic refresh.
// Closing the connection discards the schedule with the socket loop.
type liveReconcileSchedule struct {
	pending bool
	delay   time.Duration
	next    time.Time
}

func (s *liveReconcileSchedule) request(now time.Time) bool {
	if s.next.IsZero() || (!s.pending && !now.Before(s.next)) {
		s.pending = false
		s.delay = liveReconcileInitialDelay
		s.next = now.Add(s.delay)
		return true
	}
	s.pending = true
	return false
}

// due sends only for actual loss. A quiet cooldown resets pacing, while loss
// during the cooldown retains a final recovery even if no more events arrive.
func (s *liveReconcileSchedule) due(now time.Time) bool {
	if s.next.IsZero() || now.Before(s.next) {
		return false
	}
	if !s.pending {
		*s = liveReconcileSchedule{}
		return false
	}
	s.pending = false
	s.delay *= 2
	if s.delay > liveReconcileMaxDelay {
		s.delay = liveReconcileMaxDelay
	}
	s.next = now.Add(s.delay)
	return true
}
