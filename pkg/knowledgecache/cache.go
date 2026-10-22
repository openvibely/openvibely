// Package knowledgecache caches library projections and invalidates them on disk changes.
package knowledgecache

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type entry struct {
	roots []string
	value any
}
type Cache struct {
	roots   map[string]bool
	mu      sync.Mutex
	watcher *fsnotify.Watcher
	entries map[string]entry
	watched map[string]bool
	pending map[string]bool
	delay   time.Duration
	done    chan struct{}
	exited  chan struct{}
	once    sync.Once
}

func New(delay time.Duration) (*Cache, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	c := &Cache{exited: make(chan struct{}), roots: map[string]bool{}, watcher: w, entries: map[string]entry{}, watched: map[string]bool{}, pending: map[string]bool{}, delay: delay, done: make(chan struct{})}
	go c.run()
	return c, nil
}
func (c *Cache) Close() { c.once.Do(func() { close(c.done); c.watcher.Close(); <-c.exited }) }
func within(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func (c *Cache) invalidateLocked(path string) {
	for key, e := range c.entries {
		for _, root := range e.roots {
			if within(path, root) || within(root, path) {
				delete(c.entries, key)
				break
			}
		}
	}
}
func (c *Cache) Invalidate(path string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalidateLocked(filepath.Clean(path))
}
func (c *Cache) addTree(root string) bool {
	ok := true
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			ok = false
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if !c.watched[path] {
			if c.watcher.Add(path) == nil {
				c.watched[path] = true
			} else {
				ok = false
			}
		}
		return nil
	})
	return ok
}

// Watch a parent as well so replacing or creating the root does not lose coverage.
func (c *Cache) watchRoot(root string) bool {
	parent := filepath.Dir(root)
	for {
		if info, err := os.Stat(parent); err == nil && info.IsDir() {
			break
		}
		next := filepath.Dir(parent)
		if next == parent {
			return false
		}
		parent = next
	}
	if !c.watched[parent] {
		if c.watcher.Add(parent) != nil {
			return false
		}
		c.watched[parent] = true
	}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return true
	}
	return c.addTree(root)
}

// Get builds under the lock so a concurrent invalidation cannot publish stale data.
// Failure to establish watches disables caching for that entry.
func (c *Cache) Get(key string, roots []string, build func() any) any {
	if c == nil {
		return build()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		return e.value
	}
	watched := true
	for _, root := range roots {
		if root != "" {
			c.roots[root] = true
		}
		if root != "" && !c.watchRoot(root) {
			watched = false
		}
	}
	value := build()
	if watched {
		c.entries[key] = entry{append([]string(nil), roots...), value}
	}
	return value
}
func (c *Cache) run() {
	defer close(c.exited)
	var timer *time.Timer
	var tick <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-c.done:
			return
		case e, ok := <-c.watcher.Events:
			if !ok {
				return
			}
			c.mu.Lock()
			if e.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				for path := range c.watched {
					if within(path, e.Name) {
						delete(c.watched, path)
					}
				}
			}
			if e.Op&fsnotify.Create != 0 {
				for root := range c.roots {
					if within(e.Name, root) {
						c.addTree(e.Name)
					} else if within(root, e.Name) {
						c.watchRoot(root)
					}
				}
			}
			c.pending[e.Name] = true
			c.mu.Unlock()
			if tick == nil {
				timer = time.NewTimer(c.delay)
				tick = timer.C
			}
		case _, ok := <-c.watcher.Errors:
			if !ok {
				return
			}
			c.mu.Lock()
			clear(c.entries)
			c.mu.Unlock()
		case <-tick:
			c.mu.Lock()
			for path := range c.pending {
				c.invalidateLocked(path)
			}
			clear(c.pending)
			c.mu.Unlock()
			tick = nil
		}
	}
}

var sharedMu sync.Mutex
var shared *Cache

// Shared owns one watcher for the application, not one per conversation.
func Shared() *Cache {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if shared == nil {
		shared, _ = New(10 * time.Second)
	}
	return shared
}
func Invalidate(path string) {
	if path != "" {
		Shared().Invalidate(path)
	}
}
func CloseShared() {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if shared != nil {
		shared.Close()
		shared = nil
	}
}
