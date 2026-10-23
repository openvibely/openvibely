//go:build openvibely_memory_diagnostics && darwin && memory.counters

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
	"modernc.org/libc"
)

func TestDesktopModerncAllocatorStats(t *testing.T) {
	tls := libc.NewTLS()
	defer tls.Close()
	p := libc.Xmalloc(tls, 1<<20)
	require.NotZero(t, p)
	defer libc.Xfree(tls, p)
	stats, ok := desktopModerncAllocatorStats().(libc.MemAllocatorStat)
	require.True(t, ok)
	require.Positive(t, stats.Allocs)
	require.GreaterOrEqual(t, stats.Bytes, 1<<20)
	require.Positive(t, stats.Mmaps)
}
