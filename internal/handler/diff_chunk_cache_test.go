package handler

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiffChunkCacheSharesPreparation(t *testing.T) {
	var cache diffChunkCache
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	value := &preparedDiffChunks{revision: "v1", bytes: 1024}
	load := func() (*preparedDiffChunks, error) {
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
			got, err := cache.get(context.Background(), "task", 0, true, "v1", load)
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
	// Revision, task, file and review mode are separate identities.
	for _, key := range []struct {
		task     string
		index    int
		review   bool
		revision string
	}{{"task", 0, true, "v2"}, {"other", 0, true, "v1"}, {"task", 1, true, "v1"}, {"task", 0, false, "v1"}, {"task", 0, true, ""}, {"task", 0, true, ""}} {
		_, err := cache.get(context.Background(), key.task, key.index, key.review, key.revision, load)
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 7 {
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
	if cache.lookup("new", now.Add(diffChunkCacheTTL+4*time.Second)) != nil || cache.bytes != 0 {
		t.Fatal("expired entries retained")
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
	_, err := cache.get(context.Background(), "task", 0, true, "v1", func() (*preparedDiffChunks, error) { return nil, expected })
	if !errors.Is(err, expected) {
		t.Fatal(err)
	}
	value := &preparedDiffChunks{bytes: 1}
	got, err := cache.get(context.Background(), "task", 0, true, "v1", func() (*preparedDiffChunks, error) { return value, nil })
	if err != nil || got != value {
		t.Fatal("failed load poisoned cache")
	}
}

func TestDiffChunkCacheWaiterCancellation(t *testing.T) {
	var cache diffChunkCache
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_, err := cache.get(context.Background(), "task", 0, true, "v1", func() (*preparedDiffChunks, error) {
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
	_, err := cache.get(ctx, "task", 0, true, "v1", func() (*preparedDiffChunks, error) {
		t.Error("duplicate preparation")
		return nil, errors.New("unexpected")
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("waiter cancellation: %v", err)
	}
	close(release)
	<-done
}
