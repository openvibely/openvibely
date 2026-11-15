package handler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLiveReconcileSustainedLossBacksOffAndRetainsFinalRecovery(t *testing.T) {
	var s liveReconcileSchedule
	now := time.Unix(1000, 0)
	require.False(t, s.due(now), "idle connections must not reconcile")
	require.True(t, s.request(now), "first loss recovers immediately")
	for _, wait := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 15 * time.Second} {
		for i := 0; i < 1000; i++ {
			require.False(t, s.request(now.Add(wait/2)), "more drops cannot bypass cooldown")
		}
		require.False(t, s.due(now.Add(wait-time.Nanosecond)))
		now = now.Add(wait)
		require.True(t, s.due(now), "retain one recovery even after drops stop")
		require.False(t, s.due(now), "do not send the pending recovery twice")
	}
	// No additional drops: the cooldown expires without sending anything.
	now = now.Add(liveReconcileMaxDelay)
	require.False(t, s.due(now))
	require.True(t, s.request(now.Add(time.Second)))
	require.Equal(t, liveReconcileInitialDelay, s.delay, "quiet period resets backoff")
}

func TestLiveReconcilePacesAcrossSubscriptionsAfterOwnershipCheck(t *testing.T) {
	now := time.Unix(1000, 0)
	var s liveReconcileSchedule
	owners := map[string]*liveSubscription{}
	writes := 0
	write := func(f liveFrame) bool {
		if f.Event == "reconcile" && !s.request(now) {
			return true
		}
		writes++
		return true
	}
	for _, id := range []string{"task", "chat", "file", "computer"} {
		owners[id] = &liveSubscription{stop: func() {}}
		require.True(t, dispatchLiveFrame(owners, liveFrame{ID: id, Event: "reconcile", subscription: owners[id]}, write))
	}
	require.Equal(t, 1, writes, "one connection-wide recovery, not one per subscription")
	require.True(t, s.pending)
	// Normal traffic remains immediate during recovery cooldown.
	require.True(t, dispatchLiveFrame(owners, liveFrame{ID: "task", Event: "task_status_changed", subscription: owners["task"]}, write))
	require.Equal(t, 2, writes)
	require.True(t, s.due(now.Add(time.Second)))
	// Retired workers cannot dirty the coordinator or extend its cooldown.
	require.True(t, dispatchLiveFrame(owners, liveFrame{ID: "task", Event: "reconcile", subscription: &liveSubscription{}}, write))
	require.False(t, s.pending)
}

func TestLiveReconcileResetsAfterQuietPeriodWithoutTick(t *testing.T) {
	var s liveReconcileSchedule
	now := time.Unix(1000, 0)
	require.True(t, s.request(now))
	// Request and tick are selected independently by the socket loop.
	require.True(t, s.request(now.Add(2*time.Second)))
	require.Equal(t, liveReconcileInitialDelay, s.delay)
}
