package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

func sharedUsageFixture(t *testing.T, n int) (*UsageAnalyticsService, []UsageAccountDescriptor) {
	t.Helper()
	env := teststores.New(t)
	for i := 0; i < n; i++ {
		cfg := models.LLMConfig{Name: fmt.Sprint(i), Provider: models.ProviderOpenAI, Model: "test", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "fixture", OAuthAccountID: fmt.Sprint(i), OAuthExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
		if err := env.LLMConfigs.Create(context.Background(), &cfg); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"available_count":0,"credits":[]}`)) }))
	t.Cleanup(server.Close)
	old := openaiclient.OpenAIChatGPTAPIBaseURL
	openaiclient.OpenAIChatGPTAPIBaseURL = server.URL + "/backend-api/codex/"
	t.Cleanup(func() { openaiclient.OpenAIChatGPTAPIBaseURL = old })
	svc := NewUsageAnalyticsService(env.Usage, env.LLMConfigs)
	accounts, err := svc.ListUsageAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return svc, accounts
}
func waitShared(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for shared usage")
		case <-ticker.C:
		}
	}
}

func TestSharedUsageEightAccountsAndReplacement(t *testing.T) {
	svc, accounts := sharedUsageFixture(t, 8)
	started := make(chan string, 100)
	release := make(chan struct{}, 100)
	var mu sync.Mutex
	active := map[string]int{}
	maxActive := 0
	calls := 0
	overlap := false
	svc.SetAccountUsageFetcher(func(ctx context.Context, cfg models.LLMConfig) (*models.AccountUsageResult, error) {
		mu.Lock()
		active[cfg.ID]++
		calls++
		if active[cfg.ID] > 1 {
			overlap = true
		}
		total := 0
		for _, n := range active {
			total += n
		}
		if total > maxActive {
			maxActive = total
		}
		mu.Unlock()
		defer func() { mu.Lock(); active[cfg.ID]--; mu.Unlock() }()
		started <- cfg.ID
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
		}
		p := 40.0
		return &models.AccountUsageResult{PrimaryLabel: "Weekly", PrimaryUsedPercent: &p}, nil
	})
	var evaluations atomic.Int32
	c := svc.EnableSharedAccountUsage(context.Background(), nil, func(context.Context, string, *models.AnalyticsUsageAccountLimitsViewModel, map[string]any) error {
		evaluations.Add(1)
		return nil
	})
	defer c.Stop()
	if _, err := c.Read(context.Background(), "", false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("missing initial read")
		}
	}
	mu.Lock()
	initialCalls := calls
	mu.Unlock()
	if initialCalls != 4 {
		t.Fatalf("initial calls %d", initialCalls)
	}
	release <- struct{}{}
	waitShared(t, func() bool {
		snap, _ := c.Read(context.Background(), "", false)
		ready := 0
		for _, s := range snap {
			if !s.UpdatedAt.IsZero() {
				ready++
			}
		}
		return ready == 1
	})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("fifth account didn't start")
	}
	if _, err := c.Read(context.Background(), "", true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		release <- struct{}{}
	}
	waitShared(t, func() bool {
		snap, _ := c.Read(context.Background(), "", false)
		for _, a := range accounts {
			if snap[a.Key].Refreshing || snap[a.Key].UpdatedAt.IsZero() {
				return false
			}
		}
		return true
	})
	waitShared(t, func() bool { return evaluations.Load() == 9 })
	mu.Lock()
	defer mu.Unlock()
	if maxActive > 4 || overlap {
		t.Fatalf("max=%d per-account overlap=%v", maxActive, overlap)
	}
}

func TestSharedUsageIgnoresLateCancelledResult(t *testing.T) {
	svc, accounts := sharedUsageFixture(t, 1)
	started := make(chan int, 4)
	release := make(chan struct{})
	var calls atomic.Int32
	svc.SetAccountUsageFetcher(func(ctx context.Context, cfg models.LLMConfig) (*models.AccountUsageResult, error) {
		n := int(calls.Add(1))
		started <- n
		if n == 1 {
			<-release
		}
		p := float64(n * 10)
		return &models.AccountUsageResult{PrimaryLabel: "Weekly", PrimaryUsedPercent: &p}, nil
	})
	var evaluations atomic.Int32
	c := svc.EnableSharedAccountUsage(context.Background(), nil, func(context.Context, string, *models.AnalyticsUsageAccountLimitsViewModel, map[string]any) error {
		evaluations.Add(1)
		return nil
	})
	defer c.Stop()
	c.Read(context.Background(), "", false)
	<-started
	c.Read(context.Background(), accounts[0].Key, true)
	if calls.Load() != 1 {
		t.Fatal("replacement overlapped cancelled fetch")
	}
	close(release)
	waitShared(t, func() bool {
		snap, _ := c.Read(context.Background(), "", false)
		s := snap[accounts[0].Key]
		return !s.Refreshing && len(s.AccountLimits) > 0
	})
	snap, _ := c.Read(context.Background(), "", false)
	if got := *snap[accounts[0].Key].AccountLimits[0].Limits[0].UsedPercent; got != 20 {
		t.Fatalf("late result published: %v", got)
	}
	waitShared(t, func() bool { return evaluations.Load() == 1 })
}

func TestSharedUsageResetCannotBeCancelledByRefresh(t *testing.T) {
	svc, accounts := sharedUsageFixture(t, 1)
	var calls atomic.Int32
	svc.SetAccountUsageFetcher(func(context.Context, models.LLMConfig) (*models.AccountUsageResult, error) {
		calls.Add(1)
		p := 50.0
		return &models.AccountUsageResult{PrimaryLabel: "Weekly", PrimaryUsedPercent: &p}, nil
	})
	c := svc.EnableSharedAccountUsage(context.Background(), nil, nil)
	defer c.Stop()
	key := accounts[0].Key
	c.Read(context.Background(), key, false)
	waitShared(t, func() bool { s, _ := c.Read(context.Background(), key, false); return !s[key].Refreshing })
	resetStarted := make(chan struct{})
	finish := make(chan struct{})
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, err := c.reset(ctx, key, func(work context.Context) (map[string]any, error) {
			close(resetStarted)
			<-finish
			return nil, work.Err()
		})
		done <- err
	}()
	<-resetStarted
	cancel()
	before := calls.Load()
	c.Read(context.Background(), key, true)
	c.Read(context.Background(), key, true)
	if calls.Load() != before {
		t.Fatal("read started while resetting")
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatalf("reset was cancelled: %v", err)
	}
	waitShared(t, func() bool {
		s, _ := c.Read(context.Background(), key, false)
		return !s[key].Refreshing && calls.Load() == before+1
	})
}

func TestSharedUsageRetainsStaleResultAndEvictsDisconnectedAccount(t *testing.T) {
	svc, accounts := sharedUsageFixture(t, 1)
	var fail atomic.Bool
	svc.SetAccountUsageFetcher(func(context.Context, models.LLMConfig) (*models.AccountUsageResult, error) {
		if fail.Load() {
			return nil, fmt.Errorf("provider failed")
		}
		p := 70.0
		return &models.AccountUsageResult{PrimaryLabel: "Weekly", PrimaryUsedPercent: &p}, nil
	})
	c := svc.EnableSharedAccountUsage(context.Background(), nil, nil)
	defer c.Stop()
	key := accounts[0].Key
	c.Read(context.Background(), key, false)
	waitShared(t, func() bool { s, _ := c.Read(context.Background(), key, false); return !s[key].Refreshing })
	fail.Store(true)
	c.Read(context.Background(), key, true)
	waitShared(t, func() bool {
		s, _ := c.Read(context.Background(), key, false)
		return !s[key].Refreshing && s[key].Error != "" && len(s[key].AccountLimits) > 0
	})
	configs, _ := svc.llmConfigRepo.List(context.Background())
	for _, cfg := range configs {
		if cfg.AuthMethod == models.AuthMethodOAuth {
			if err := svc.llmConfigRepo.Delete(context.Background(), cfg.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	snap, err := c.Read(context.Background(), "", false)
	if err != nil || len(snap) != 0 {
		t.Fatalf("disconnected snapshot retained: %v %v", snap, err)
	}
}

func TestSharedUsageManualRefreshEvaluatesAndDeduplicatesAlerts(t *testing.T) {
	svc, accounts := sharedUsageFixture(t, 1)
	env := teststores.New(t)
	broadcaster := events.NewBroadcaster()
	sub, _ := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(sub)
	monitor := NewAccountUsageMonitor(svc, env.Stores.Settings, env.Stores.Projects, NewAlertService(env.Stores.Alerts, broadcaster))
	svc.SetAccountUsageFetcher(func(context.Context, models.LLMConfig) (*models.AccountUsageResult, error) {
		p := 96.0
		window := 300
		reset := time.Now().Add(time.Hour).Truncate(time.Hour).Format(time.RFC3339)
		return &models.AccountUsageResult{PrimaryLabel: "Session", PrimaryUsedPercent: &p, PrimaryWindowMinutes: &window, PrimaryResetsAt: &reset}, nil
	})
	var evaluated atomic.Int32
	c := svc.EnableSharedAccountUsage(context.Background(), nil, func(ctx context.Context, key string, v *models.AnalyticsUsageAccountLimitsViewModel, r map[string]any) error {
		err := monitor.EvaluateSnapshot(ctx, key, v, r)
		if err != nil {
			t.Error(err)
		}
		evaluated.Add(1)
		return err
	})
	defer c.Stop()
	c.Read(context.Background(), accounts[0].Key, true)
	waitShared(t, func() bool { return evaluated.Load() == 1 })
	select {
	case ev := <-sub:
		if ev.AlertSource != "account_usage" {
			t.Fatal(ev)
		}
	case <-time.After(time.Second):
		t.Fatal("manual refresh did not emit usage alert")
	}
	c.Read(context.Background(), accounts[0].Key, true)
	waitShared(t, func() bool { return evaluated.Load() == 2 })
	select {
	case ev := <-sub:
		t.Fatalf("duplicate alert: %+v", ev)
	default:
	}
}

func TestSharedUsageLegacyReportingReplacesRead(t *testing.T) {
	svc, accounts := sharedUsageFixture(t, 1)
	started := make(chan struct{}, 4)
	var calls, active, overlap, evaluated, published atomic.Int32
	release := make(chan struct{})
	svc.SetAccountUsageFetcher(func(ctx context.Context, cfg models.LLMConfig) (*models.AccountUsageResult, error) {
		if active.Add(1) > 1 {
			overlap.Add(1)
		}
		defer active.Add(-1)
		n := calls.Add(1)
		started <- struct{}{}
		if n == 1 {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
		}
		p := 31.0
		return &models.AccountUsageResult{PrimaryUsedPercent: &p}, nil
	})
	c := svc.EnableSharedAccountUsage(context.Background(), func() { published.Add(1) }, func(context.Context, string, *models.AnalyticsUsageAccountLimitsViewModel, map[string]any) error {
		evaluated.Add(1)
		return nil
	})
	defer c.Stop()
	c.Read(context.Background(), "", true)
	<-started
	result := make(chan error, 1)
	go func() {
		view, err := svc.BuildLiveAnalyticsUsage(context.Background(), persistence.UsageFilter{AccountGroupKey: accounts[0].Key})
		if err == nil && (len(view.AccountLimits) != 1 || view.AccountLimits[0].Error != "") {
			err = fmt.Errorf("unexpected view: %+v", view)
		}
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("reporting did not replace old read")
	}
	close(release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reporting did not complete")
	}
	if overlap.Load() != 0 || evaluated.Load() != 1 || published.Load() != 1 {
		t.Fatalf("overlap=%d evaluated=%d published=%d", overlap.Load(), evaluated.Load(), published.Load())
	}
	snapshots, err := c.Read(context.Background(), "", false)
	if err != nil || len(snapshots[accounts[0].Key].AccountLimits) != 1 {
		t.Fatal("missing shared result", err)
	}
}

func TestSharedUsageModelChangesPreserveCoordination(t *testing.T) {
	for _, duringReset := range []bool{false, true} {
		for _, remove := range []bool{false, true} {
			t.Run(fmt.Sprintf("reset=%t/remove=%t", duringReset, remove), func(t *testing.T) {
				svc, accounts := sharedUsageFixture(t, 1)
				alias := models.LLMConfig{Name: "alias", Provider: models.ProviderOpenAI, Model: "other", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "fixture", OAuthAccountID: "0", OAuthExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
				if remove {
					if err := svc.llmConfigRepo.Create(context.Background(), &alias); err != nil {
						t.Fatal(err)
					}
					accounts, _ = svc.ListUsageAccounts(context.Background())
				}
				gate := make(chan struct{})
				started := make(chan struct{}, 10)
				var calls atomic.Int32
				svc.SetAccountUsageFetcher(func(ctx context.Context, cfg models.LLMConfig) (*models.AccountUsageResult, error) {
					n := calls.Add(1)
					started <- struct{}{}
					if !duringReset && n == 1 {
						<-gate
					} // Simulate a provider that takes time to honour cancellation.
					p := 40.0
					return &models.AccountUsageResult{PrimaryUsedPercent: &p}, nil
				})
				c := svc.EnableSharedAccountUsage(context.Background(), nil, nil)
				defer c.Stop()
				var release sync.Once
				defer release.Do(func() { close(gate) })
				c.Read(context.Background(), "", true)
				<-started
				if duringReset {
					waitShared(t, func() bool {
						c.mu.Lock()
						defer c.mu.Unlock()
						return !c.entries[c.accountKeys[accounts[0].Key]].snapshot.Refreshing
					})
				}
				c.mu.Lock()
				entry := c.entries[c.accountKeys[accounts[0].Key]]
				c.mu.Unlock()
				resetStarted := make(chan struct{})
				resetFinished := make(chan error, 1)
				if duringReset {
					go func() {
						_, err := c.reset(context.Background(), accounts[0].Key, func(ctx context.Context) (map[string]any, error) { close(resetStarted); <-gate; return nil, nil })
						resetFinished <- err
					}()
					<-resetStarted
				}
				if remove {
					if err := svc.llmConfigRepo.Delete(context.Background(), alias.ID); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := svc.llmConfigRepo.Create(context.Background(), &alias); err != nil {
						t.Fatal(err)
					}
				}
				current, err := svc.ListUsageAccounts(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if current[0].StableKey == "" || current[0].StableKey != accounts[0].StableKey {
					t.Fatal("model change altered stable account key")
				}
				if current[0].Key == accounts[0].Key {
					t.Fatal("fixture did not change inventory key")
				}
				snapshots, err := c.Read(context.Background(), "", true)
				if err != nil {
					t.Fatal(err)
				}
				c.mu.Lock()
				same := c.entries[c.accountKeys[current[0].Key]] == entry
				c.mu.Unlock()
				if !same {
					t.Fatal("scheduler replaced account entry")
				}
				if duringReset && len(snapshots[current[0].Key].AccountLimits) == 0 {
					t.Fatal("model change lost cached usage")
				}
				select {
				case <-started:
					t.Fatal("replacement overlapped old fetch or reset")
				case <-time.After(50 * time.Millisecond):
				}
				release.Do(func() { close(gate) })
				if duringReset {
					if err := <-resetFinished; err != nil {
						t.Fatal(err)
					}
				}
				waitShared(t, func() bool {
					c.mu.Lock()
					defer c.mu.Unlock()
					e := c.entries[c.accountKeys[current[0].Key]]
					return !e.snapshot.Refreshing && len(e.snapshot.AccountLimits) > 0
				})
			})
		}
	}
}
