package repokey

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func waitCache(t *testing.T, c *Cache) {
	t.Helper()
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, e := range c.entries {
			if e.running {
				return false
			}
		}
		return true
	}, time.Second, time.Millisecond)
}
func advance(c *Cache, d time.Duration) {
	c.mu.Lock()
	old := c.clock()
	c.clock = func() time.Time { return old.Add(d) }
	c.mu.Unlock()
}

func TestCacheRefreshFailureAndRemoval(t *testing.T) {
	c := NewCache()
	var calls atomic.Int32
	var fail atomic.Bool
	var value atomic.Value
	value.Store("example.com/team/project")
	c.resolve = func(string, string) (string, error) {
		calls.Add(1)
		if fail.Load() {
			return "", errors.New("unavailable")
		}
		return value.Load().(string), nil
	}
	changes, stop := c.Subscribe()
	defer stop()
	require.Empty(t, c.Get("1", "", "/project"))
	waitCache(t, c)
	require.Equal(t, "example.com/team/project", c.Get("1", "", "/project"))
	require.EqualValues(t, 1, calls.Load())
	select {
	case <-changes:
	default:
		t.Fatal("missing identity notification")
	}
	for range 20 {
		require.NotEmpty(t, c.Get("1", "", "/project"))
	}
	require.EqualValues(t, 1, calls.Load())
	advance(c, 15*time.Minute)
	fail.Store(true)
	require.NotEmpty(t, c.Get("1", "", "/project"))
	waitCache(t, c)
	require.EqualValues(t, 2, calls.Load())
	advance(c, 4*time.Second)
	c.Get("1", "", "/project")
	require.EqualValues(t, 2, calls.Load())
	advance(c, time.Second)
	fail.Store(false)
	value.Store("")
	c.Get("1", "", "/project")
	waitCache(t, c)
	require.Empty(t, c.Get("1", "", "/project"))
	require.EqualValues(t, 3, calls.Load())
	select {
	case <-changes:
	default:
		t.Fatal("missing removal notification")
	}
	advance(c, 59*time.Second)
	c.Get("1", "", "/project")
	require.EqualValues(t, 3, calls.Load())
	advance(c, time.Second)
	c.Get("1", "", "/project")
	waitCache(t, c)
	require.EqualValues(t, 4, calls.Load())
}
func TestCacheSingleLookupAndInvalidation(t *testing.T) {
	c := NewCache()
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	c.resolve = func(string, string) (string, error) {
		n := calls.Add(1)
		started <- struct{}{}
		<-release
		return fmt.Sprint(n), nil
	}
	c.Get("1", "", "/project")
	<-started
	for range 20 {
		c.Get("2", "", "/project")
	}
	require.EqualValues(t, 1, calls.Load())
	c.Changed("1")
	c.Get("1", "", "/project")
	<-started
	close(release)
	waitCache(t, c)
	require.Equal(t, "2", c.Get("1", "", "/project"))
	require.Equal(t, "example.com/team/project", c.Get("3", "https://example.com/team/project.git", "/unused"))
	require.EqualValues(t, 2, calls.Load())
}
func TestCacheCapacityAndRetain(t *testing.T) {
	c := NewCache()
	c.resolve = func(string, string) (string, error) { return "identity", nil }
	for i := range 512 {
		c.Get(fmt.Sprint(i), "", fmt.Sprint(i))
		waitCache(t, c)
	}
	c.Get("0", "", "0") // Keep the oldest inserted entry hot.
	c.Get("new", "", "new")
	waitCache(t, c)
	c.mu.Lock()
	require.Len(t, c.entries, 512)
	require.Contains(t, c.entries, cacheKey{"", "0"})
	require.NotContains(t, c.entries, cacheKey{"", "1"})
	c.mu.Unlock()
	c.Get("alias", "", "0")
	c.Retain(map[string]bool{"alias": true})
	require.Equal(t, "identity", c.Get("alias", "", "0"))
	c.mu.Lock()
	require.Len(t, c.entries, 1)
	c.mu.Unlock()
	c.Retain(map[string]bool{})
	c.mu.Lock()
	require.Empty(t, c.entries)
	require.Empty(t, c.projects)
	c.mu.Unlock()
}

func TestCacheProjectEditsAndSharedRemoval(t *testing.T) {
	c := NewCache()
	var calls atomic.Int32
	c.resolve = func(string, string) (string, error) { calls.Add(1); return "example.com/team/project", nil }
	c.Get("1", "", "/project")
	waitCache(t, c)
	c.Get("2", "", "/project")
	c.Updated("1", "", "/project")
	require.Equal(t, "example.com/team/project", c.Get("1", "", "/project"))
	require.EqualValues(t, 1, calls.Load(), "ordinary edits must not reread Git")
	c.Removed("1")
	require.Equal(t, "example.com/team/project", c.Get("2", "", "/project"))
	require.EqualValues(t, 1, calls.Load(), "another project still uses the repository")
	c.Updated("2", "https://example.com/team/other.git", "/other")
	require.Equal(t, "example.com/team/other", c.Get("2", "https://example.com/team/other.git", "/other"))
	c.mu.Lock()
	require.Empty(t, c.entries, "unused old location should be removed")
	c.mu.Unlock()
}

func TestCachePendingAndRetainedIdentity(t *testing.T) {
	c := NewCache()
	started, release := make(chan struct{}, 2), make(chan struct{}, 2)
	var result atomic.Value
	result.Store("example.com/team/project")
	c.resolve = func(string, string) (string, error) {
		started <- struct{}{}
		<-release
		return result.Load().(string), nil
	}
	value, pending := c.State("1", "", "/project", true)
	require.Empty(t, value)
	require.True(t, pending)
	<-started
	release <- struct{}{}
	waitCache(t, c)
	value, pending = c.State("1", "", "/project", true)
	require.Equal(t, "example.com/team/project", value)
	require.False(t, pending)
	c.Changed("1")
	value, pending = c.State("1", "", "/project", true)
	require.Equal(t, "example.com/team/project", value)
	require.True(t, pending)
	<-started
	result.Store("")
	release <- struct{}{}
	waitCache(t, c)
	value, pending = c.State("1", "", "/project", true)
	require.Empty(t, value)
	require.False(t, pending, "a completed empty lookup differs from pending")
}

func TestCacheEvictionDoesNotRescanOrForgetProjects(t *testing.T) {
	c := NewCache()
	var calls atomic.Int32
	c.resolve = func(string, string) (string, error) { calls.Add(1); return "example.com/team/project", nil }
	changes, stop := c.Subscribe()
	defer stop()
	for i := 0; i < 513; i++ {
		c.Get(fmt.Sprint(i), "", fmt.Sprint(i))
		waitCache(t, c)
	}
	select {
	case <-changes:
	default:
	}
	for round := 0; round < 10; round++ {
		for i := 0; i < 513; i++ {
			id := fmt.Sprint(i)
			value, pending := c.State(id, "", id, true)
			require.Equal(t, "example.com/team/project", value)
			require.False(t, pending)
		}
	}
	require.EqualValues(t, 513, calls.Load())
	select {
	case <-changes:
		t.Fatal("eviction generated an identity change")
	default:
	}
	_, pending := c.State("new", "", "/new", false)
	require.True(t, pending)
	require.EqualValues(t, 513, calls.Load(), "notification reads never start Git work")
	c.Retain(map[string]bool{})
	require.Empty(t, c.states, "deleted project projections must be removed")
}

func TestInitialFailureStopsLoadingAndRecovers(t *testing.T) {
	c := NewCache()
	var calls atomic.Int32
	c.resolve = func(string, string) (string, error) {
		if calls.Add(1) == 1 {
			return "", errors.New("unreadable")
		}
		return "example.com/team/project", nil
	}
	changes, stop := c.Subscribe()
	defer stop()
	_, pending := c.State("1", "", "/project", true)
	require.True(t, pending)
	waitCache(t, c)
	value, pending := c.State("1", "", "/project", true)
	require.Empty(t, value)
	require.False(t, pending, "failed is not loading")
	select {
	case <-changes:
	default:
		t.Fatal("loading completion must notify clients")
	}
	advance(c, 4*time.Second)
	c.Get("1", "", "/project")
	require.EqualValues(t, 1, calls.Load())
	advance(c, time.Second)
	c.Get("1", "", "/project")
	waitCache(t, c)
	value, pending = c.State("1", "", "/project", true)
	require.Equal(t, "example.com/team/project", value)
	require.False(t, pending)
	require.EqualValues(t, 2, calls.Load())
	select {
	case <-changes:
	default:
		t.Fatal("recovery must notify clients")
	}
}
