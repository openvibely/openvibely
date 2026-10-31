package handler

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openvibely/openvibely/web/templates/components"
	"golang.org/x/sync/singleflight"
)

// Versioned requests read immutable snapshots, so scrolling never mixes code
// versions. New viewer revisions use new keys. Unversioned requests stay live.
const diffChunkCacheBytes int64 = 128 << 20
const diffChunkCacheEntries = 32
const diffChunkCacheTTL = 5 * time.Minute
const diffChunkPreparationTimeout = 30 * time.Second

type preparedDiffChunks struct {
	path, revision string
	index          int
	inline, split  []components.DiffFile
	bytes          int64
}

func prepareDiffChunks(meta components.DiffFileRenderMeta, revision string) *preparedDiffChunks {
	p := &preparedDiffChunks{path: meta.File.Path, index: meta.Index, revision: revision}
	// Parser strings can reference the full multi-file diff. Detach the selected
	// file so retaining one snapshot cannot retain an unaccounted whole-task buffer.
	file := meta.File
	file.Path, file.OldPath = strings.Clone(file.Path), strings.Clone(file.OldPath)
	file.Hunks = append([]components.DiffHunk(nil), file.Hunks...)
	for i := range file.Hunks {
		hunk := &file.Hunks[i]
		hunk.Header = strings.Clone(hunk.Header)
		hunk.Lines = append([]components.DiffLine(nil), hunk.Lines...)
		hunk.SplitPairs = nil
		for j := range hunk.Lines {
			hunk.Lines[j].Content = strings.Clone(hunk.Lines[j].Content)
		}
	}
	p.path = file.Path
	p.inline = components.DiffFileChunks(file, "inline")
	p.split = components.DiffFileChunks(file, "split")
	// Conservative accounting includes backing arrays, split pairs, strings and
	// chunk metadata. Counting shared strings twice keeps the memory budget bounded.
	p.bytes = int64(1024 + len(p.path))
	for _, chunks := range [][]components.DiffFile{p.inline, p.split} {
		for _, chunk := range chunks {
			p.bytes += int64(512 + len(chunk.Path) + len(chunk.OldPath))
			for _, hunk := range chunk.Hunks {
				p.bytes += int64(256 + len(hunk.Header))
				for _, line := range hunk.Lines {
					p.bytes += int64(512 + len(line.Content))
				}
			}
		}
	}
	return p
}

type diffChunkCacheEntry struct {
	value         *preparedDiffChunks
	expires, used time.Time
}

type diffChunkCache struct {
	mu      sync.Mutex
	entries map[string]diffChunkCacheEntry
	bytes   int64
	flights singleflight.Group
}

func (c *diffChunkCache) lookup(key string, now time.Time) *preparedDiffChunks {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, entry := range c.entries {
		if !now.Before(entry.expires) {
			delete(c.entries, k)
			c.bytes -= entry.value.bytes
		}
	}
	entry, ok := c.entries[key]
	if !ok {
		return nil
	}
	entry.used = now
	c.entries[key] = entry
	return entry.value
}

func (c *diffChunkCache) store(key string, value *preparedDiffChunks, now time.Time) {
	if value.bytes > diffChunkCacheBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]diffChunkCacheEntry)
	}
	if old, ok := c.entries[key]; ok {
		c.bytes -= old.value.bytes
		delete(c.entries, key)
	}
	for len(c.entries) >= diffChunkCacheEntries || c.bytes+value.bytes > diffChunkCacheBytes {
		var oldest string
		var used time.Time
		for k, entry := range c.entries {
			if used.IsZero() || entry.used.Before(used) {
				oldest, used = k, entry.used
			}
		}
		c.bytes -= c.entries[oldest].value.bytes
		delete(c.entries, oldest)
	}
	c.entries[key] = diffChunkCacheEntry{value: value, expires: now.Add(diffChunkCacheTTL), used: now}
	c.bytes += value.bytes
}

func (c *diffChunkCache) get(ctx context.Context, taskID string, index int, review bool, revision string, load func(context.Context) (*preparedDiffChunks, error)) (*preparedDiffChunks, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if revision == "" {
		return load(ctx)
	}
	key := fmt.Sprintf("%q:%d:%t:%q", taskID, index, review, revision)
	if value := c.lookup(key, time.Now()); value != nil {
		return value, nil
	}
	result := c.flights.DoChan(key, func() (any, error) {
		if value := c.lookup(key, time.Now()); value != nil {
			return value, nil
		}
		// Shared work outlives any one waiter, but keeps request-scoped values
		// and has its own deadline. Each caller still waits using its own ctx.
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), diffChunkPreparationTimeout)
		defer cancel()
		type preparationResult struct {
			value *preparedDiffChunks
			err   error
		}
		completed := make(chan preparationResult, 1)
		go func() {
			value, err := load(loadCtx)
			completed <- preparationResult{value, err}
		}()
		var value *preparedDiffChunks
		var err error
		select {
		case <-loadCtx.Done():
			return nil, loadCtx.Err()
		case result := <-completed:
			value, err = result.value, result.err
		}
		if loadCtx.Err() != nil {
			return nil, loadCtx.Err()
		}
		if err == nil {
			c.store(key, value, time.Now())
		}
		return value, err
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-result:
		if result.Err != nil {
			return nil, result.Err
		}
		return result.Val.(*preparedDiffChunks), nil
	}
}
