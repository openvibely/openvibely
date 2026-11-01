package database

import (
	"context"
	"testing"
	"time"
)

func TestVacuumAnyFreePagesAndFormerThresholdCrossings(t *testing.T) {
	for _, fixture := range []struct {
		name, deletion string
		crossing       bool
	}{
		{"one_free_page", "DELETE FROM payload WHERE rowid = 1", false},
		{"below_both", "DELETE FROM payload WHERE rowid <= 20", false},
		{"cross_both", "DELETE FROM payload WHERE rowid <= 3000", true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			db := vacuumFixtureWithDelete(t, 6000, true, fixture.deletion)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			timer := &triggerTimer{make(chan time.Time, 1), make(chan time.Duration, 1)}
			results := make(chan vacuumResult, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				runVacuum(ctx, timer, vacuumInterval, func(pages int) vacuumResult {
					r := reclaimBatch(ctx, db, pages, nil)
					results <- r
					return r
				}, func(int64) int64 { return 0 })
			}()
			defer func() { cancel(); receiveTriggerTest(t, done) }()
			belowCount, belowPercent := false, false
			remaining := int64(-1)
			for batch := 0; batch < 200; batch++ {
				timer.ticks <- time.Now()
				r := receiveTriggerTest(t, results)
				delay := receiveTriggerTest(t, timer.resets)
				if r.err != nil || r.busy {
					t.Fatalf("batch failed: %+v", r)
				}
				if batch == 0 {
					if fixture.name == "one_free_page" && r.before.free != 1 {
						t.Fatalf("expected one free page, got %d", r.before.free)
					}
					aboveBoth := r.before.free >= 2500 && r.before.free*100/r.before.pages >= 10
					belowBoth := r.before.free > 0 && r.before.free < 2500 && r.before.free*100/r.before.pages < 10
					if (fixture.crossing && !aboveBoth) || (!fixture.crossing && !belowBoth) {
						t.Fatalf("unexpected fixture: %+v", r.before)
					}
				}
				if r.before.free == 0 {
					if r.attempted || delay != vacuumInterval {
						t.Fatalf("empty freelist did not idle: %+v delay=%s", r, delay)
					}
					if !belowCount || !belowPercent {
						t.Fatalf("no progress below old thresholds: count=%t percent=%t", belowCount, belowPercent)
					}
					// No automatic retry when empty. A manually delivered fallback tick
					// performs one check and returns to the same idle interval.
					select {
					case <-results:
						t.Fatal("empty freelist spun")
					default:
					}
					timer.ticks <- time.Now()
					empty := receiveTriggerTest(t, results)
					if empty.attempted || empty.before.free != 0 {
						t.Fatalf("empty fallback: %+v", empty)
					}
					if d := receiveTriggerTest(t, timer.resets); d != vacuumInterval {
						t.Fatal(d)
					}
					return
				}
				if remaining < 0 {
					remaining = r.before.free
				}
				if !r.progress() {
					t.Fatalf("free pages did not make progress: %+v", r)
				}
				remaining -= r.before.pages - r.after.pages
				if delay == vacuumInterval {
					if remaining > 0 && r.after.free > 0 {
						t.Fatalf("stopped before budget or free pages exhausted: remaining=%d result=%+v", remaining, r)
					}
					remaining = -1
				}
				belowCount = belowCount || r.before.free < 2500
				belowPercent = belowPercent || r.before.free*100/r.before.pages < 10
				// Budget exhaustion can leave metadata-related free pages for a fresh
				// fallback run. Neither former threshold may terminate a run early.
				if delay != 0 && delay != vacuumInterval {
					t.Fatalf("unexpected delay %s", delay)
				}
			}
			t.Fatal("cleanup did not finish")
		})
	}
}
