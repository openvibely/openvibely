package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

const accountMonitorLease = "account_usage_monitor:lease"

type AccountUsageMonitor struct {
	usage    *UsageAnalyticsService
	settings persistence.SettingsStore
	projects persistence.ProjectStore
	alerts   *AlertService
	mu       sync.Mutex
	wg       sync.WaitGroup
}

func NewAccountUsageMonitor(u *UsageAnalyticsService, s persistence.SettingsStore, p persistence.ProjectStore, a *AlertService) *AccountUsageMonitor {
	return &AccountUsageMonitor{usage: u, settings: s, projects: p, alerts: a}
}

// Trigger never delays credential refresh and drops overlapping timer ticks.
func (m *AccountUsageMonitor) Trigger(ctx context.Context) {
	if !m.mu.TryLock() {
		return
	}
	m.wg.Add(1)
	go func() {
		defer m.mu.Unlock()
		defer m.wg.Done()
		ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		defer cancel()
		if err := m.RunOnce(ctx); err != nil && ctx.Err() == nil {
			applog.Infof("[account-usage-monitor] %v", err)
		}
	}()
}
func (m *AccountUsageMonitor) Stop() { m.wg.Wait() }

type monitoredGrant struct {
	Remaining  int
	Generation int
	Expires    string
}
type monitoredLimit struct {
	Window    string
	Cycle     int
	Percent   float64
	Threshold int
}
type accountMonitorState struct {
	ResetsInitialized bool
	Grants            map[string]monitoredGrant
	Limits            map[string]monitoredLimit
	Sent              map[string]bool
}
type accountMonitorEvent struct {
	Key, Title, Message string
	Severity            models.AlertSeverity
}

func monitorHash(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }

func (m *AccountUsageMonitor) RunOnce(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	prior, err := m.settings.Get(ctx, accountMonitorLease)
	if err != nil {
		return err
	}
	until, _ := strconv.ParseInt(prior, 10, 64)
	if time.Now().UnixMilli() < until {
		return nil
	}
	lease := strconv.FormatInt(time.Now().Add(5*time.Minute).UnixMilli(), 10)
	acquired, err := m.settings.CompareAndSet(ctx, accountMonitorLease, prior, lease, nil)
	if err != nil || !acquired {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = m.settings.CompareAndSet(cleanup, accountMonitorLease, lease, "", nil)
	}()
	projects, err := m.projects.List(ctx)
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		return nil
	}
	configs, err := m.usage.llmConfigRepo.List(ctx)
	if err != nil {
		return err
	}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	var errsMu sync.Mutex
	var errs []string
	for _, provider := range []string{"openai", "anthropic"} {
		for _, group := range accountUsageGroups(configs, provider) {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				wg.Wait()
				return ctx.Err()
			}
			wg.Add(1)
			go func(group []models.LLMConfig) {
				defer wg.Done()
				defer func() { <-sem }()
				if err := m.checkAccount(ctx, group, projects); err != nil {
					errsMu.Lock()
					errs = append(errs, err.Error())
					errsMu.Unlock()
				}
			}(group)
		}
	}
	wg.Wait()
	if len(errs) > 0 {
		return fmt.Errorf("account checks: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (m *AccountUsageMonitor) checkAccount(ctx context.Context, group []models.LLMConfig, projects []models.Project) error {
	cfg := group[0]
	key := usageAccountGroupKey(group)
	identity := accountUsageKeyForConfig(cfg)
	stateKey := "account_usage_monitor:state:" + monitorHash(identity)
	raw, err := m.settings.Get(ctx, stateKey)
	if err != nil {
		return err
	}
	state := accountMonitorState{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			return fmt.Errorf("invalid monitor state: %w", err)
		}
	}
	if state.Grants == nil {
		state.Grants = map[string]monitoredGrant{}
	}
	if state.Limits == nil {
		state.Limits = map[string]monitoredLimit{}
	}
	if state.Sent == nil {
		state.Sent = map[string]bool{}
	}
	var limits *models.AnalyticsUsageAccountLimitsViewModel
	var resets map[string]any
	var limitErr, resetErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		limits, limitErr = m.usage.BuildAnalyticsAccountLimits(ctx, persistence.UsageFilter{AccountGroupKey: key, Refresh: true})
	}()
	go func() { defer wg.Done(); resets, resetErr = m.usage.UsageResetCredits(ctx, key) }()
	wg.Wait()
	label := accountMonitorLabel(group, limits)
	var rows []models.AccountLimitView
	if limitErr == nil && limits != nil {
		for _, a := range limits.AccountLimits {
			if a.Error == "" {
				rows = append(rows, a.Limits...)
			}
		}
	}
	if resetErr != nil {
		resets = nil
	}
	events := evaluateAccountMonitor(&state, rows, resets, time.Now(), label)
	for _, event := range events {
		for _, project := range projects {
			eventKey := monitorHash(identity + "\x00" + event.Key)
			sentKey := project.ID + ":" + eventKey
			if state.Sent[sentKey] {
				continue
			}
			link := "/analytics?view=usage&project_id=" + url.QueryEscape(project.ID) + "&account_key=" + url.QueryEscape(key)
			alert := &models.Alert{ProjectID: project.ID, Type: models.AlertCustom, Source: "account_usage", Severity: event.Severity, Title: event.Title, Message: event.Message, Body: event.Message, IdempotencyKey: "account-usage:" + eventKey, Metadata: map[string]any{"account_key": key, "usage_url": link}}
			if err := m.alerts.CreateAccountUsageAlert(ctx, alert, link); err != nil {
				return err
			}
			state.Sent[sentKey] = true
		}
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err = m.settings.Set(ctx, stateKey, string(encoded)); err != nil {
		return err
	}
	if limitErr != nil {
		return limitErr
	}
	return resetErr
}

// Use the same successful account view as Analytics, never a model alias.
// Reset alerts can still be emitted when limits are unavailable; in that case
// only use a stored identity if the group has a single credential owner.
func accountMonitorLabel(group []models.LLMConfig, limits *models.AnalyticsUsageAccountLimitsViewModel) string {
	provider := "OpenAI"
	if group[0].Provider == models.ProviderAnthropic {
		provider = "Anthropic"
	}
	identity := accountUsageGroupDisplayIdentity(group, nil)
	plan := ""
	if limits != nil {
		for _, account := range limits.AccountLimits {
			if account.Error != "" {
				continue
			}
			identity = strings.TrimSpace(account.AccountIdentity)
			plan = strings.TrimSpace(account.PlanType)
			break
		}
	}
	var details []string
	if identity != "" && identity != accountUsageProviderFallbackIdentity(group[0].Provider) && identity != "Connected account" {
		details = append(details, identity)
	}
	if plan != "" {
		details = append(details, plan)
	}
	if len(details) > 0 {
		return provider + " (" + strings.Join(details, " · ") + ")"
	}
	return provider
}

func evaluateAccountMonitor(state *accountMonitorState, limits []models.AccountLimitView, resets map[string]any, now time.Time, label string) []accountMonitorEvent {
	var events []accountMonitorEvent
	for _, limit := range limits {
		if limit.UsedPercent == nil {
			continue
		}
		pct := *limit.UsedPercent
		key := limit.LimitKey
		if key == "" {
			key = limit.Label
		}
		prev := state.Limits[key]
		window := ""
		if limit.ResetsAt != nil {
			window = *limit.ResetsAt
		}
		cycle := prev.Cycle
		if window == "" && pct <= 80 && prev.Percent > 80 {
			cycle++
		}

		threshold := 0
		if pct >= 100 {
			threshold = 100
		} else if pct > 95 {
			threshold = 95
		} else if pct > 80 {
			threshold = 80
		}
		if prev.Window == window && prev.Cycle == cycle && prev.Threshold > threshold {
			threshold = prev.Threshold
		}
		state.Limits[key] = monitoredLimit{Window: window, Cycle: cycle, Percent: pct, Threshold: threshold}
		if threshold > 0 && pct > 80 {
			eventKey := fmt.Sprintf("limit:%s:%s:%d:%d", key, window, cycle, threshold)
			// Keep the reported percentage, including fractions: 99.9% is not 100%.
			percentage := strconv.FormatFloat(pct, 'f', -1, 64) + "%"
			limitName := strings.TrimSpace(limit.Label)
			if limitName == "" {
				limitName = "Account"
			}
			title := fmt.Sprintf("%s — %s: %s used (nearing limit)", label, limitName, percentage)
			severity := models.SeverityWarning
			if pct >= 100 {
				title = fmt.Sprintf("%s — %s: limit reached (%s used)", label, limitName, percentage)
				severity = models.SeverityError
			}
			events = append(events, accountMonitorEvent{eventKey, title, fmt.Sprintf("%s is %s used.", limitName, percentage), severity})
		}
	}
	credits, ok := resets["credits"].([]any)
	if !ok {
		return events
	}
	seenIDs := map[string]bool{}
	represented := 0
	for _, item := range credits {
		credit, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := credit["id"].(string)
		if id == "" || credit["status"] != "available" {
			continue
		}
		remaining := 1
		if v, ok := credit["remaining_uses"].(int); ok {
			remaining = v
		} else if v, ok := credit["remaining_uses"].(float64); ok {
			remaining = int(v)
		}
		seenIDs[id] = true
		represented += remaining
		if remaining <= 0 {
			grant := state.Grants[id]
			grant.Remaining = 0
			state.Grants[id] = grant
			continue
		}
		title, _ := credit["title"].(string)
		if title == "" {
			title = "Usage reset"
		}
		expires := normalizeResetTime(firstPresent(credit, "expires_at", "expiresAt"))
		prev, seen := state.Grants[id]
		generation := prev.Generation
		if expiry, err := time.Parse(time.RFC3339, expires); err == nil && !expiry.After(now) {
			continue
		}
		if state.ResetsInitialized && (!seen || remaining > prev.Remaining) {
			generation++
			events = append(events, accountMonitorEvent{fmt.Sprintf("grant:%s:%d", id, generation), label + " received a usage reset", fmt.Sprintf("%s now has %d use(s) remaining.", title, remaining), models.SeverityInfo})
		}
		state.Grants[id] = monitoredGrant{remaining, generation, expires}
		if expiry, err := time.Parse(time.RFC3339, expires); err == nil && expiry.After(now) {
			hours := expiry.Sub(now).Hours()
			threshold := 0
			if hours <= 2 {
				threshold = 2
			} else if hours <= 24 {
				threshold = 24
			}
			if threshold > 0 {
				events = append(events, accountMonitorEvent{fmt.Sprintf("expiry:%s:%s:%d", id, expires, threshold), label + " reset expires soon", fmt.Sprintf("%s expires at %s; %d use(s) remain.", title, expiry.UTC().Format(time.RFC3339), remaining), models.SeverityWarning})
			}
		}
	}
	// Only a complete provider list proves a missing grant has no uses left.
	count := represented
	if v, ok := resets["available_count"].(float64); ok {
		count = int(v)
	} else if v, ok := resets["available_count"].(int); ok {
		count = v
	} else if v, ok := resets["availableCount"].(float64); ok {
		count = int(v)
	}
	if count <= represented {
		for id, grant := range state.Grants {
			if !seenIDs[id] {
				grant.Remaining = 0
				state.Grants[id] = grant
			}
		}
	}
	state.ResetsInitialized = true
	return events
}
