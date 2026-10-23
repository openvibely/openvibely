//go:build openvibely_memory_diagnostics && darwin && memory.counters

package main

import "modernc.org/libc"

// These are direct allocator mappings, outside the Go heap. Bytes includes
// retained mappings and must not be presented as live SQLite object bytes.
func desktopModerncAllocatorStats() any {
	return libc.MemStat()
}
