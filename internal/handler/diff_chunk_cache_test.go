package handler

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestDiffChunkCacheSharesPreparation(t *testing.T) {
	var cache diffChunkCache
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	value := &preparedDiffChunks{revision: "v1", bytes: 1024}
	load := func(context.Context) (*preparedDiffChunks, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		return value, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := cache.get(context.Background(), "task", 0, "v1", load)
			if err != nil || got != value {
				t.Errorf("get=%p,%v", got, err)
			}
		}()
	}
	<-entered
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("prepared %d times", calls.Load())
	}
	// Revision, task and file are separate identities.
	for _, key := range []struct {
		task     string
		index    int
		revision string
	}{{"task", 0, "v2"}, {"other", 0, "v1"}, {"task", 1, "v1"}, {"task", 0, ""}, {"task", 0, ""}} {
		_, err := cache.get(context.Background(), key.task, key.index, key.revision, load)
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 6 {
		t.Fatalf("unexpected identity reuse: %d loads", calls.Load())
	}
}

func TestDiffChunkCacheBoundsAndExpiry(t *testing.T) {
	var cache diffChunkCache
	now := time.Now()
	value := &preparedDiffChunks{bytes: diffChunkCacheBytes / 2}
	cache.store("old", value, now)
	cache.store("recent", value, now.Add(time.Second))
	cache.lookup("old", now.Add(2*time.Second))
	cache.store("new", value, now.Add(3*time.Second))
	if cache.lookup("recent", now.Add(4*time.Second)) != nil {
		t.Fatal("LRU entry not evicted")
	}
	if cache.bytes > diffChunkCacheBytes || len(cache.entries) != 2 {
		t.Fatal("cache exceeded memory budget")
	}
	cache.store("oversized", &preparedDiffChunks{bytes: diffChunkCacheBytes + 1}, now)
	if cache.lookup("oversized", now) != nil {
		t.Fatal("oversized entry retained")
	}
	if cache.lookup("new", now.Add(diffChunkCacheTTL+4*time.Second)) != nil {
		t.Fatal("expired entry returned")
	}
	if _, ok := cache.entries["new"]; ok || cache.bytes != value.bytes {
		t.Fatal("expired entry retained")
	}
	for i := 0; i < diffChunkCacheEntries+5; i++ {
		cache.store(string(rune(i)), &preparedDiffChunks{bytes: 1}, now.Add(time.Duration(i)*time.Millisecond))
	}
	if len(cache.entries) != diffChunkCacheEntries {
		t.Fatal("entry bound not enforced")
	}
}

func TestDiffChunkCacheDoesNotCacheFailures(t *testing.T) {
	var cache diffChunkCache
	expected := errors.New("source unavailable")
	_, err := cache.get(context.Background(), "task", 0, "v1", func(context.Context) (*preparedDiffChunks, error) { return nil, expected })
	if !errors.Is(err, expected) {
		t.Fatal(err)
	}
	value := &preparedDiffChunks{bytes: 1}
	got, err := cache.get(context.Background(), "task", 0, "v1", func(context.Context) (*preparedDiffChunks, error) { return value, nil })
	if err != nil || got != value {
		t.Fatal("failed load poisoned cache")
	}
}

func TestDiffChunkCacheWaiterCancellation(t *testing.T) {
	var cache diffChunkCache
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_, err := cache.get(context.Background(), "task", 0, "v1", func(context.Context) (*preparedDiffChunks, error) {
			close(entered)
			<-release
			return &preparedDiffChunks{bytes: 1}, nil
		})
		if err != nil {
			t.Error(err)
		}
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := cache.get(ctx, "task", 0, "v1", func(context.Context) (*preparedDiffChunks, error) {
		t.Error("duplicate preparation")
		return nil, errors.New("unexpected")
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("waiter cancellation: %v", err)
	}
	close(release)
	<-done
}

func TestDiffChunkCacheLeaderCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cache diffChunkCache
		type contextKey struct{}
		leaderCtx, cancel := context.WithTimeout(context.WithValue(context.Background(), contextKey{}, "scope"), time.Second)
		defer cancel()
		started, release := make(chan context.Context, 1), make(chan struct{})
		leaderDone, waiterDone := make(chan error, 1), make(chan error, 1)
		value := &preparedDiffChunks{bytes: 1}
		go func() {
			_, err := cache.get(leaderCtx, "task", 0, "v1", func(loadCtx context.Context) (*preparedDiffChunks, error) {
				started <- loadCtx
				select {
				case <-release:
					return value, nil
				case <-loadCtx.Done():
					return nil, loadCtx.Err()
				}
			})
			leaderDone <- err
		}()
		loadCtx := <-started
		if loadCtx.Value(contextKey{}) != "scope" {
			t.Fatal("shared context lost request values")
		}
		if deadline, ok := loadCtx.Deadline(); !ok || time.Until(deadline) != diffChunkPreparationTimeout {
			t.Fatal("missing independent preparation deadline")
		}
		go func() {
			got, err := cache.get(context.Background(), "task", 0, "v1", func(context.Context) (*preparedDiffChunks, error) {
				t.Error("duplicate preparation")
				return value, nil
			})
			if err == nil && got != value {
				t.Error("wrong shared result")
			}
			waiterDone <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-leaderDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled leader: %v", err)
		}
		if loadCtx.Err() != nil {
			t.Fatalf("leader cancelled shared preparation: %v", loadCtx.Err())
		}
		close(release)
		if err := <-waiterDone; err != nil {
			t.Fatalf("live waiter failed: %v", err)
		}
		_, err := cache.get(context.Background(), "task", 0, "v1", func(context.Context) (*preparedDiffChunks, error) {
			t.Error("completed snapshot was not cached")
			return nil, errors.New("unexpected reload")
		})
		if err != nil {
			t.Fatal(err)
		}
		if loadCtx.Err() != context.Canceled {
			t.Fatal("preparation context not released")
		}
	})
}

func TestDiffChunkCachePreparationTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cache diffChunkCache
		_, err := cache.get(context.Background(), "task", 0, "v1", func(loadCtx context.Context) (*preparedDiffChunks, error) {
			<-loadCtx.Done()
			// Even a loader returning a late success must not populate the cache.
			return &preparedDiffChunks{bytes: 1}, nil
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("preparation deadline: %v", err)
		}
		calls := 0
		_, err = cache.get(context.Background(), "task", 0, "v1", func(context.Context) (*preparedDiffChunks, error) { calls++; return &preparedDiffChunks{bytes: 1}, nil })
		if err != nil || calls != 1 {
			t.Fatal("timed-out preparation poisoned cache")
		}
	})
}

func TestDiffChunkCacheTimeoutBoundsUnresponsiveLoader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cache diffChunkCache
		release := make(chan struct{})
		defer close(release)
		done := make(chan error, 1)
		go func() {
			_, err := cache.get(context.Background(), "task", 0, "v1", func(context.Context) (*preparedDiffChunks, error) {
				<-release
				return &preparedDiffChunks{bytes: 1}, nil
			})
			done <- err
		}()
		synctest.Wait()
		time.Sleep(diffChunkPreparationTimeout + time.Second)
		synctest.Wait()
		select {
		case err := <-done:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected deadline: %v", err)
			}
		default:
			t.Fatal("waiter remained blocked after preparation deadline")
		}
		// A retry must start fresh even while the old operation is still unwinding.
		fresh := &preparedDiffChunks{bytes: 2}
		got, err := cache.get(context.Background(), "task", 0, "v1", func(context.Context) (*preparedDiffChunks, error) { return fresh, nil })
		if err != nil || got != fresh {
			t.Fatalf("retry failed: %v", err)
		}
	})
}
