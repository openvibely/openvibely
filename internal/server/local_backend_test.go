package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type localTestScheduler struct{ starts, stops atomic.Int32 }

func (s *localTestScheduler) Start(context.Context) { s.starts.Add(1) }
func (s *localTestScheduler) Stop()                 { s.stops.Add(1) }

func TestLocalServicesLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler := &localTestScheduler{}
	var starts, stops, active atomic.Int32
	l := &localBackend{ctx: ctx, scheduler: scheduler, active: func() int { return int(active.Load()) }, startServices: func() { starts.Add(1) }, stopServices: func() { stops.Add(1) }}
	l.start()
	require.Zero(t, starts.Load(), "off startup must not start services")
	require.NoError(t, l.SetEnabled(true))
	require.NoError(t, l.SetEnabled(true))
	require.EqualValues(t, 1, starts.Load())
	active.Store(1)
	require.NoError(t, l.SetEnabled(false))
	require.False(t, l.Enabled())
	require.Equal(t, "stopping", l.Mode())
	require.Zero(t, stops.Load(), "active work keeps dependent services alive")
	// Re-enable during drain: the old waiter must not stop the resumed services.
	require.NoError(t, l.SetEnabled(true))
	active.Store(0)
	require.Never(t, func() bool { return stops.Load() != 0 || l.Mode() != "on" }, 250*time.Millisecond, 10*time.Millisecond)
	require.NoError(t, l.SetEnabled(false))
	require.Eventually(t, func() bool { return l.Mode() == "off" }, time.Second, time.Millisecond)
	require.EqualValues(t, 1, stops.Load())
	require.NoError(t, l.SetEnabled(true))
	active.Store(1)
	require.NoError(t, l.SetEnabled(false))
	active.Store(0)
	require.Eventually(t, func() bool { return l.Mode() == "off" }, time.Second, time.Millisecond)
	require.EqualValues(t, 2, starts.Load())
	require.EqualValues(t, 2, stops.Load())
	l.shutdown()
	require.EqualValues(t, 2, stops.Load(), "shutdown must not stop services twice")
}

func TestLocalServicesPersistenceFailureLeavesServicesRunning(t *testing.T) {
	scheduler := &localTestScheduler{}
	l := &localBackend{ctx: context.Background(), scheduler: scheduler, persist: func(bool) error { return errors.New("disk failure") }}
	l.enabled.Store(true)
	l.start()
	require.Error(t, l.SetEnabled(false))
	require.True(t, l.Enabled())
	require.Zero(t, scheduler.stops.Load())
	l.shutdown()
}
