package service

import (
	"context"
	"sync"
	"time"

	"github.com/openvibely/openvibely/internal/llm/preparedhistory"
	"github.com/openvibely/openvibely/internal/models"
)

// The budget includes an allowance for decoded strings and mutable containers.
// It is an estimate of retained data, not a process-memory limit.
const activeCheckpointBudget = 64 << 20
const activeCheckpointIdle = 5 * time.Minute

type checkpointRevisionStore interface {
	GetChatCompactionCheckpointIfChanged(context.Context, string, string, string) (*models.ChatCompactionCheckpoint, string, error)
}
type activeCheckpointEntry struct {
	checkpoint models.ChatCompactionCheckpoint
	revision   string
	prepared   *preparedhistory.ReusableState
	cost       int
	used       time.Time
	timer      *time.Timer
}
type activeCheckpointCache struct {
	idle    time.Duration
	mu      sync.Mutex
	entries map[[2]string]*activeCheckpointEntry
}

func cloneActiveCheckpoint(c models.ChatCompactionCheckpoint) *models.ChatCompactionCheckpoint {
	c.History = append([]models.Execution(nil), c.History...)
	for i := range c.History {
		e := &c.History[i]
		if e.DisplayTail != nil {
			tail := *e.DisplayTail
			e.DisplayTail = &tail
		}
		e.ReplayMessages = append([]models.ExecutionReplayMessage(nil), e.ReplayMessages...)
		e.RetainedUserMessage = append([]byte(nil), e.RetainedUserMessage...)
		if e.CompletedAt != nil {
			t := *e.CompletedAt
			e.CompletedAt = &t
		}
	}
	// ReplayRows are immutable, including their envelope and item byte slices.
	return &c
}
func checkpointCost(c *models.ChatCompactionCheckpoint) int {
	n := len(c.ProviderStateJSON) + len(c.ProviderSessionStateJSON) + len(c.Summary)
	rows := func(r *models.ReplayRows) int {
		if r == nil {
			return 0
		}
		size := 0
		for _, v := range r.Items {
			size += len(v) + 256
		}
		for _, v := range r.Envelope {
			size += len(v)
		}
		return size
	}
	n += rows(c.ProviderStateRows)
	for _, e := range c.History {
		n += len(e.PromptSent) + len(e.DiffOutput) + len(e.ErrorMessage) + len(e.Output) + len(e.ReasoningContent) + len(e.RetainedUserMessage) + 1024
		for _, r := range e.ReplayMessages {
			n += len(r.TranscriptJSON) + len(r.UserContent) + len(r.AssistantContent) + len(r.ReasoningContent) + rows(r.Rows)
		}
	}
	return 3*n + 1024
}
func (s *LLMService) loadActiveCheckpoint(ctx context.Context, scopeType, scopeID string) (*models.ChatCompactionCheckpoint, *preparedhistory.ReusableState, error) {
	store, ok := s.execRepo.(checkpointRevisionStore)
	if !ok {
		c, e := s.execRepo.GetChatCompactionCheckpoint(ctx, scopeType, scopeID)
		return c, nil, e
	}
	cache := &s.checkpointCache
	key := [2]string{scopeType, scopeID}
	cache.mu.Lock()
	old := cache.entries[key]
	revision := ""
	if old != nil {
		revision = old.revision
	}
	cache.mu.Unlock()
	c, version, err := store.GetChatCompactionCheckpointIfChanged(ctx, scopeType, scopeID, revision)
	if err != nil {
		return nil, nil, err
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if c == nil && version != "" && old != nil && version == old.revision {
		// A concurrent update may have replaced the cached entry. This request still
		// owns a valid snapshot; never reinstall it over the newer one.
		if cache.entries[key] == old {
			cache.touch(key, old)
		}
		return cloneActiveCheckpoint(old.checkpoint), old.prepared, nil
	}
	if c == nil || version == "" {
		if cache.entries[key] == old {
			cache.remove(key)
		}
		return c, nil, nil
	}
	cost := checkpointCost(c)
	if cost > activeCheckpointBudget {
		if cache.entries[key] == old {
			cache.remove(key)
		}
		return c, nil, nil
	}
	entry := &activeCheckpointEntry{checkpoint: *cloneActiveCheckpoint(*c), revision: version, cost: cost, prepared: preparedhistory.NewReusableState()}
	entry.prepared.BindReplay(c.NativeState())
	// Do not overwrite a concurrently installed snapshot from a different read.
	if cache.entries[key] != old {
		return c, nil, nil
	}
	cache.remove(key)
	total := cost
	for _, e := range cache.entries {
		total += e.cost
	}
	for len(cache.entries) >= 8 || total > activeCheckpointBudget {
		var oldest [2]string
		var selected *activeCheckpointEntry
		for k, e := range cache.entries {
			if selected == nil || e.used.Before(selected.used) {
				oldest, selected = k, e
			}
		}
		if selected == nil {
			break
		}
		total -= selected.cost
		cache.remove(oldest)
	}
	if cache.entries == nil {
		cache.entries = make(map[[2]string]*activeCheckpointEntry)
	}
	cache.entries[key] = entry
	cache.touch(key, entry)
	return c, entry.prepared, nil
}
func (c *activeCheckpointCache) remove(key [2]string) {
	if e := c.entries[key]; e != nil {
		if e.timer != nil {
			e.timer.Stop()
		}
		delete(c.entries, key)
	}
}
func (c *activeCheckpointCache) touch(key [2]string, e *activeCheckpointEntry) {
	e.used = time.Now()
	if e.timer != nil {
		e.timer.Stop()
	}
	used := e.used
	idle := c.idle
	if idle == 0 {
		idle = activeCheckpointIdle
	}
	e.timer = time.AfterFunc(idle, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.entries[key] == e && e.used == used {
			c.remove(key)
		}
	})
}
