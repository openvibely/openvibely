//go:build openvibely_memory_diagnostics && darwin && !memory.counters

package main

// Omit unavailable counters rather than reporting misleading zeros.
func desktopModerncAllocatorStats() any { return nil }
