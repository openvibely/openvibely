package repokey

import (
	"context"
	"sync"
	"time"
)

type cacheKey struct{ url, path string }
type identityState struct {
	key      cacheKey
	value    string
	resolved bool
	failed   bool
	expires  time.Time
}

type cacheEntry struct {
	value    string
	expires  time.Time
	used     uint64
	running  bool
	resolved bool
	failed   bool
}

// Cache belongs to one server's project store. It never caches permissions.
// Lookups are lazy and nonblocking; a previous value remains visible during refresh.
type Cache struct {
	mu       sync.Mutex
	entries  map[cacheKey]*cacheEntry
	projects map[string]cacheKey
	states   map[string]identityState
	watchers map[chan struct{}]struct{}
	clock    func() time.Time
	resolve  func(string, string) (string, error)
	tick     uint64
	slots    chan struct{}
}

func NewCache() *Cache {
	return &Cache{entries: map[cacheKey]*cacheEntry{}, projects: map[string]cacheKey{}, states: map[string]identityState{}, watchers: map[chan struct{}]struct{}{}, clock: time.Now, resolve: resolveIdentity, slots: make(chan struct{}, 4)}
}

// State is the last project identity, independent of bounded lookup entries.
// Evicting a lookup must not erase the project projection or change its identity.
func (c *Cache) Get(id, url, path string) string {
	value, _ := c.State(id, url, path, true)
	return value
}

func (c *Cache) State(id, url, path string, lookup bool) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := cacheKey{url, path}
	c.projects[id] = key
	state := c.states[id]
	if state.key != key {
		state = identityState{key: key, value: state.value}
	}
	if value := Normalize(url); value != "" || path == "" {
		state.value, state.resolved, state.failed = value, true, false
		c.states[id] = state
		return state.value, false
	}
	c.states[id] = state
	c.tick++
	if entry := c.entries[key]; entry != nil {
		entry.used = c.tick
	}
	if !lookup || c.clock().Before(state.expires) {
		return state.value, !state.resolved && !state.failed
	}
	c.tick++
	entry := c.entries[key]
	if entry == nil {
		if len(c.entries) >= 512 {
			var oldest cacheKey
			min := ^uint64(0)
			found := false
			for k, e := range c.entries {
				if !e.running && e.used < min {
					oldest, min, found = k, e.used, true
				}
			}
			if !found {
				return state.value, !state.resolved && !state.failed
			}
			delete(c.entries, oldest)
		}
		entry = &cacheEntry{value: state.value, expires: state.expires, resolved: state.resolved, failed: state.failed}
		c.entries[key] = entry
	}
	entry.used = c.tick
	if !entry.running && !c.clock().Before(entry.expires) {
		entry.running = true
		go c.load(key, entry)
	} else if !entry.running {
		state.value, state.resolved, state.expires, state.failed = entry.value, entry.resolved, entry.expires, entry.failed
		c.states[id] = state
	}
	return state.value, !state.resolved && !state.failed
}

type cachedOnlyKey struct{}

// CachedOnly prevents notification reads from scheduling more Git lookups.
func CachedOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, cachedOnlyKey{}, true)
}
func LookupAllowed(ctx context.Context) bool {
	cached, _ := ctx.Value(cachedOnlyKey{}).(bool)
	return !cached
}

func (c *Cache) load(key cacheKey, entry *cacheEntry) {
	c.slots <- struct{}{}
	value, err := c.resolve(key.url, key.path)
	<-c.slots
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries[key] != entry {
		return
	}
	entry.running = false
	entry.failed = err != nil
	ttl := 15 * time.Minute
	if err != nil {
		ttl = 5 * time.Second
	} else if value == "" {
		ttl = time.Minute
	}
	entry.expires = c.clock().Add(ttl)
	changed := false
	if err == nil {
		entry.value = value
		entry.resolved = true
	}
	for id, projectKey := range c.projects {
		if projectKey != key {
			continue
		}
		state := c.states[id]
		state.key, state.expires = key, entry.expires
		if err == nil {
			changed = changed || !state.resolved || state.failed || state.value != value
			state.value, state.resolved, state.failed = value, true, false
		}
		if err != nil {
			changed = changed || (!state.resolved && !state.failed)
			state.failed = true
		}
		c.states[id] = state
	}
	if changed {
		c.notifyLocked()
	}

}

// Changed invalidates a project's previous location and wakes project-list subscribers.
// A late lookup for the old location cannot overwrite its replacement.
func (c *Cache) Changed(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if key, ok := c.projects[id]; ok {
		delete(c.entries, key)
		for projectID, projectKey := range c.projects {
			if projectKey == key {
				state := c.states[projectID]
				state.expires = time.Time{}
				state.resolved = false
				state.failed = false
				c.states[projectID] = state
			}
		}
	}
	c.notifyLocked()
}

// Updated preserves an unchanged repository entry while notifying project-list readers.
func (c *Cache) Updated(id, url, path string) {
	c.mu.Lock()
	key := cacheKey{url, path}
	if previous, ok := c.projects[id]; ok && previous != key {
		delete(c.projects, id)
		c.dropUnusedLocked(previous)
	}
	c.projects[id] = key
	if state := c.states[id]; state.key != key {
		c.states[id] = identityState{key: key, value: state.value}
	}
	c.notifyLocked()
	c.mu.Unlock()
	c.Get(id, url, path)
}

func (c *Cache) Removed(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.states, id)
	if key, ok := c.projects[id]; ok {
		delete(c.projects, id)
		c.dropUnusedLocked(key)
	}
	c.notifyLocked()
}

func (c *Cache) dropUnusedLocked(key cacheKey) {
	for _, used := range c.projects {
		if used == key {
			return
		}
	}
	delete(c.entries, key)
}

// Retain drops references to deleted projects, while preserving shared repository entries.
func (c *Cache) Retain(ids map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.projects {
		if !ids[id] {
			delete(c.projects, id)
			delete(c.states, id)
		}
	}
	used := map[cacheKey]bool{}
	for _, key := range c.projects {
		used[key] = true
	}
	for key := range c.entries {
		if !used[key] {
			delete(c.entries, key)
		}
	}
}

func (c *Cache) Subscribe() (<-chan struct{}, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan struct{}, 1)
	c.watchers[ch] = struct{}{}
	return ch, func() { c.mu.Lock(); delete(c.watchers, ch); c.mu.Unlock() }
}
func (c *Cache) notifyLocked() {
	for ch := range c.watchers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
