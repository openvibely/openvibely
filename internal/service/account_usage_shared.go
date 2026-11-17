package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// AccountUsageSnapshot is the latest server-owned result, never usage history.
// Published values are immutable; readers must not mutate nested provider data.
type AccountUsageSnapshot struct {
	AccountLimits []models.AccountUsageView `json:"account_limits"`
	Resets        map[string]any            `json:"resets,omitempty"`
	Error         string                    `json:"error,omitempty"`
	ResetError    string                    `json:"reset_error,omitempty"`
	Refreshing    bool                      `json:"refreshing"`
	UpdatedAt     time.Time                 `json:"updated_at,omitempty"`
}
type accountUsageEntry struct {
	fingerprint string
	snapshot    AccountUsageSnapshot
	cancel      context.CancelFunc
	done        chan struct{}
	generation  uint64
	resetMu     sync.Mutex
	resetDone   chan struct{}
}

// SharedAccountUsage owns the scheduler and all account read generations. A
// replacement waits for its predecessor to exit before taking a concurrency slot.
type SharedAccountUsage struct {
	usage    *UsageAnalyticsService
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	entries  map[string]*accountUsageEntry
	slots    chan struct{}
	wg       sync.WaitGroup
	stopped  bool
	updated  func()
	evaluate func(context.Context, string, *models.AnalyticsUsageAccountLimitsViewModel, map[string]any) error
}

func (s *UsageAnalyticsService) EnableSharedAccountUsage(ctx context.Context, updated func(), evaluate func(context.Context, string, *models.AnalyticsUsageAccountLimitsViewModel, map[string]any) error) *SharedAccountUsage {
	ctx, cancel := context.WithCancel(ctx)
	c := &SharedAccountUsage{usage: s, ctx: ctx, cancel: cancel, entries: map[string]*accountUsageEntry{}, slots: make(chan struct{}, 4), updated: updated, evaluate: evaluate}
	s.sharedUsage = c
	return c
}
func (c *SharedAccountUsage) Start() {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.refreshAll()
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-c.ctx.Done():
				return
			case <-ticker.C:
				c.refreshAll()
			}
		}
	}()
}
func (c *SharedAccountUsage) Stop() {
	c.mu.Lock()
	c.stopped = true
	c.cancel()
	c.mu.Unlock()
	c.wg.Wait()
}
func (c *SharedAccountUsage) refreshAll() {
	if _, err := c.Read(c.ctx, "", true); err != nil && c.ctx.Err() == nil {
		applog.Infof("[account-usage] background refresh: %v", err)
	}
}

// Read validates inventory on every call, evicts disconnected accounts and starts
// missing reads. Refresh replaces existing work; ordinary reads only read memory.
func (c *SharedAccountUsage) Read(ctx context.Context, key string, refresh bool) (map[string]AccountUsageSnapshot, error) {
	return c.read(ctx, key, "", refresh)
}

func (c *SharedAccountUsage) read(ctx context.Context, key, provider string, refresh bool) (map[string]AccountUsageSnapshot, error) {
	accounts, err := c.usage.ListUsageAccounts(ctx)
	if err != nil {
		return nil, err
	}
	configs, err := c.usage.llmConfigRepo.List(ctx)
	if err != nil {
		return nil, err
	}
	fingerprints := map[string]string{}
	for _, provider := range []string{"openai", "anthropic"} {
		for _, group := range accountUsageGroups(configs, provider) {
			parts := []string{}
			for _, cfg := range group {
				parts = append(parts, fmt.Sprintf("%s:%s:%s:%d", cfg.ID, cfg.OAuthConnectionID, cfg.OAuthAccountID, cfg.OAuthConfigRevision))
			}
			sort.Strings(parts)
			fingerprints[usageAccountGroupKey(group)] = strings.Join(parts, "|")
		}
	}
	valid := map[string]bool{}
	for _, a := range accounts {
		valid[a.Key] = true
	}
	if key != "" && !valid[key] {
		return nil, fmt.Errorf("account configuration changed; reload accounts")
	}
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return nil, context.Canceled
	}
	changed := false
	for k, e := range c.entries {
		if !valid[k] {
			if e.cancel != nil {
				e.cancel()
			}
			delete(c.entries, k)
			changed = true
		}
	}
	result := map[string]AccountUsageSnapshot{}
	for _, a := range accounts {
		if (key != "" && a.Key != key) || (provider != "" && a.Provider != provider) {
			continue
		}
		e := c.entries[a.Key]
		if e != nil && e.fingerprint != fingerprints[a.Key] {
			changed = true
			if e.cancel != nil {
				e.cancel()
			}
			previous := e.done
			e = &accountUsageEntry{done: previous, fingerprint: fingerprints[a.Key]}
			c.entries[a.Key] = e
			c.startLocked(a.Key, e)
		} else if e == nil {
			e = &accountUsageEntry{fingerprint: fingerprints[a.Key]}
			c.entries[a.Key] = e
			c.startLocked(a.Key, e)
		} else if refresh {
			c.startLocked(a.Key, e)
		}
		result[a.Key] = e.snapshot
	}
	c.mu.Unlock()
	if changed && c.updated != nil {
		c.updated()
	}
	return result, nil
}
func (c *SharedAccountUsage) startLocked(key string, e *accountUsageEntry) {
	previous := e.done
	if e.cancel != nil {
		e.cancel()
	}
	ctx, cancel := context.WithCancel(c.ctx)
	e.cancel = cancel
	e.generation++
	generation := e.generation
	done := make(chan struct{})
	e.done = done
	e.snapshot.Refreshing = true
	resetDone := e.resetDone
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer close(done)
		defer cancel()
		// Even a cancelled generation waits for its predecessor, so repeated refreshes
		// cannot bypass the per-account exclusion chain.
		if previous != nil {
			<-previous
		}
		if resetDone != nil {
			select {
			case <-resetDone:
			case <-ctx.Done():
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
		select {
		case c.slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-c.slots }()
		ctx, timeout := context.WithTimeout(ctx, time.Minute)
		defer timeout()
		var view *models.AnalyticsUsageAccountLimitsViewModel
		var resets map[string]any
		var limitErr, resetErr error
		var reads sync.WaitGroup
		reads.Add(2)
		go func() {
			defer reads.Done()
			view, limitErr = c.usage.BuildLiveAnalyticsAccountLimits(ctx, persistence.UsageFilter{AccountGroupKey: key, Refresh: true})
		}()
		go func() { defer reads.Done(); resets, resetErr = c.usage.usageResetRequest(ctx, key, "", "") }()
		reads.Wait()
		if ctx.Err() != nil && c.ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		if c.entries[key] != e || e.generation != generation {
			c.mu.Unlock()
			return
		}
		// A cancelled timeout is an unavailable result; a superseded generation never publishes.
		if limitErr == nil && (view == nil || len(view.AccountLimits) == 0) {
			limitErr = fmt.Errorf("usage unavailable")
		}
		if limitErr == nil {
			for _, a := range view.AccountLimits {
				if a.Error != "" {
					limitErr = fmt.Errorf("%s", a.Error)
					break
				}
			}
		}
		next := e.snapshot
		next.Refreshing = false
		next.Error = ""
		next.ResetError = ""
		if limitErr != nil {
			next.Error = limitErr.Error()
		} else {
			next.AccountLimits = view.AccountLimits
			next.UpdatedAt = time.Now()
		}
		if resetErr != nil {
			next.ResetError = resetErr.Error()
			next.Resets = nil
		} else {
			next.Resets = resets
		}
		e.snapshot = next
		c.mu.Unlock()
		if c.updated != nil {
			c.updated()
		}
		if c.evaluate != nil && (limitErr == nil || resetErr == nil) && ctx.Err() == nil {
			if limitErr != nil {
				view = nil
			}
			if resetErr != nil {
				resets = nil
			}
			if err := c.evaluate(ctx, key, view, resets); err != nil && ctx.Err() == nil {
				applog.Infof("[account-usage] alert evaluation: %v", err)
			}
		}
	}()
}

// reset serializes mutations per account, independently of refresh cancellation.
func (c *SharedAccountUsage) reset(ctx context.Context, key string, fn func(context.Context) (map[string]any, error)) (map[string]any, error) {
	if _, err := c.Read(ctx, key, false); err != nil {
		return nil, err
	}
	c.mu.Lock()
	e := c.entries[key]
	c.mu.Unlock()
	if e == nil {
		return nil, fmt.Errorf("account disconnected")
	}
	e.resetMu.Lock()
	defer e.resetMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.stopped || c.entries[key] != e {
		c.mu.Unlock()
		return nil, fmt.Errorf("account configuration changed")
	}
	c.wg.Add(1)
	defer c.wg.Done()
	e.resetDone = make(chan struct{})
	if e.cancel != nil {
		e.cancel()
	}
	e.generation++
	c.mu.Unlock()
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	result, err := fn(work)
	c.mu.Lock()
	close(e.resetDone)
	e.resetDone = nil
	if !c.stopped && c.entries[key] == e {
		c.startLocked(key, e)
	}
	c.mu.Unlock()
	return result, err
}
func (s *UsageAnalyticsService) SharedAccountUsage(ctx context.Context, key string, refresh bool) (map[string]AccountUsageSnapshot, error) {
	if s.sharedUsage == nil {
		return nil, fmt.Errorf("shared usage service unavailable")
	}
	return s.sharedUsage.Read(ctx, key, refresh)
}

func (s *UsageAnalyticsService) HasSharedAccountUsage() bool { return s.sharedUsage != nil }

// freshLimits serves synchronous reporting callers through the same scheduler.
// If another refresh replaces our generation, wait for its result instead.
func (c *SharedAccountUsage) freshLimits(ctx context.Context, filter persistence.UsageFilter) (*models.AnalyticsUsageAccountLimitsViewModel, error) {
	requested, err := c.read(ctx, filter.AccountGroupKey, filter.Provider, true)
	if err != nil {
		return nil, err
	}
	accounts, err := c.usage.ListUsageAccounts(ctx)
	if err != nil {
		return nil, err
	}
	view := &models.AnalyticsUsageAccountLimitsViewModel{AccountLimits: []models.AccountUsageView{}}
	for _, account := range accounts {
		if _, ok := requested[account.Key]; !ok {
			continue
		}
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			c.mu.Lock()
			e := c.entries[account.Key]
			if e == nil || c.stopped {
				c.mu.Unlock()
				return nil, fmt.Errorf("account configuration changed or usage service stopped")
			}
			done := e.done
			resetDone := e.resetDone
			c.mu.Unlock()
			if resetDone != nil {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-resetDone:
				}
				continue
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-done:
			}
			c.mu.Lock()
			if c.entries[account.Key] != e || e.done != done || e.snapshot.Refreshing {
				c.mu.Unlock()
				continue
			}
			snapshot := e.snapshot
			c.mu.Unlock()
			if snapshot.Error != "" {
				// Reporting must not present a retained stale allowance as a fresh result.
				view.AccountLimits = append(view.AccountLimits, models.AccountUsageView{Provider: account.Provider, AccountIdentity: account.Identity, Error: snapshot.Error})
				view.Errors = append(view.Errors, snapshot.Error)
			} else {
				view.AccountLimits = append(view.AccountLimits, snapshot.AccountLimits...)
				if view.LastUpdatedAt == nil || snapshot.UpdatedAt.After(*view.LastUpdatedAt) {
					updated := snapshot.UpdatedAt
					view.LastUpdatedAt = &updated
				}
			}
			break
		}
	}
	return view, nil
}
