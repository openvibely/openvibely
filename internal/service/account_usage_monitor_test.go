package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func monitorTestState() accountMonitorState {
	return accountMonitorState{Grants: map[string]monitoredGrant{}, Limits: map[string]monitoredLimit{}, Sent: map[string]bool{}}
}
func TestAccountMonitorLabels(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider models.LLMProvider
		stored   string
		accounts []models.AccountUsageView
		multiple bool
		want     string
	}{
		{name: "OpenAI email and plan", provider: models.ProviderOpenAI, accounts: []models.AccountUsageView{{AccountIdentity: "owner@example.com", PlanType: "ChatGPT Pro"}}, want: "OpenAI (owner@example.com · ChatGPT Pro)"},
		{name: "Anthropic email and plan", provider: models.ProviderAnthropic, accounts: []models.AccountUsageView{{AccountIdentity: "owner@example.com", PlanType: "Claude Max"}}, want: "Anthropic (owner@example.com · Claude Max)"},
		{name: "name without email", provider: models.ProviderAnthropic, accounts: []models.AccountUsageView{{AccountIdentity: "Account owner", PlanType: "Pro Lite"}}, want: "Anthropic (Account owner · Pro Lite)"},
		{name: "plan only", provider: models.ProviderOpenAI, accounts: []models.AccountUsageView{{AccountIdentity: "OpenAI account", PlanType: "ChatGPT Pro"}}, want: "OpenAI (ChatGPT Pro)"},
		{name: "single stored identity on failed usage", provider: models.ProviderAnthropic, stored: "owner@example.com", accounts: []models.AccountUsageView{{Error: "refresh failed", AccountIdentity: "stale@example.com", PlanType: "Stale plan"}}, want: "Anthropic (owner@example.com)"},
		{name: "no metadata", provider: models.ProviderOpenAI, want: "OpenAI"},
		{name: "ambiguous sign-ins without usage", provider: models.ProviderAnthropic, stored: "first@example.com", multiple: true, want: "Anthropic"},
		{name: "snapshot sign-in wins", provider: models.ProviderAnthropic, stored: "first@example.com", multiple: true, accounts: []models.AccountUsageView{{Error: "failed", AccountIdentity: "first@example.com"}, {AccountIdentity: "fetcher@example.com", PlanType: "Claude Max"}}, want: "Anthropic (fetcher@example.com · Claude Max)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group := []models.LLMConfig{{Name: "Model alias", Provider: tc.provider, OAuthConnectionID: "first", OAuthProviderDisplayName: tc.stored}}
			if tc.multiple {
				group = append(group, models.LLMConfig{Name: "Other model alias", Provider: tc.provider, OAuthConnectionID: "second"})
			}
			label := accountMonitorLabel(group, &models.AnalyticsUsageAccountLimitsViewModel{AccountLimits: tc.accounts})
			if label != tc.want {
				t.Fatalf("label = %q, want %q", label, tc.want)
			}
			state := monitorTestState()
			state.ResetsInitialized = true
			now := time.Now().UTC()
			pct := 96.0
			limits := []models.AccountLimitView{{LimitKey: "weekly", Label: "Weekly", UsedPercent: &pct}}
			resets := map[string]any{"credits": []any{map[string]any{"id": "grant", "status": "available", "remaining_uses": 1, "expires_at": now.Add(time.Hour).Format(time.RFC3339)}}}
			events := evaluateAccountMonitor(&state, limits, resets, now, label)
			wantSuffixes := []string{" — Weekly: 96% used (nearing limit)", " received a usage reset", " reset expires soon"}
			if len(events) != len(wantSuffixes) {
				t.Fatalf("events = %+v", events)
			}
			for i, event := range events {
				if event.Title != tc.want+wantSuffixes[i] {
					t.Fatalf("title = %q, want %q", event.Title, tc.want+wantSuffixes[i])
				}
			}
		})
	}
}

func TestAccountMonitorReportsActualUsage(t *testing.T) {
	for _, tc := range []struct {
		pct        float64
		percentage string
		threshold  string
		reached    bool
	}{
		{pct: 79.9},
		{pct: 80},
		{pct: 81, percentage: "81%", threshold: ":80"},
		{pct: 95, percentage: "95%", threshold: ":80"},
		{pct: 95.1, percentage: "95.1%", threshold: ":95"},
		{pct: 96, percentage: "96%", threshold: ":95"},
		{pct: 99.9, percentage: "99.9%", threshold: ":95"},
		{pct: 100, percentage: "100%", threshold: ":100", reached: true},
		{pct: 101, percentage: "101%", threshold: ":100", reached: true},
	} {
		t.Run(fmt.Sprint(tc.pct), func(t *testing.T) {
			state := monitorTestState()
			limits := []models.AccountLimitView{{LimitKey: "weekly", Label: "Weekly", UsedPercent: &tc.pct}}
			events := evaluateAccountMonitor(&state, limits, nil, time.Now(), "Anthropic (owner@example.com · Claude Max)")
			if tc.threshold == "" {
				if len(events) != 0 {
					t.Fatalf("unexpected event below threshold: %+v", events)
				}
				return
			}
			if len(events) != 1 {
				t.Fatalf("want exactly one event, got %+v", events)
			}
			event := events[0]
			wantSeverity := models.SeverityWarning
			if tc.reached {
				wantSeverity = models.SeverityError
			}
			if event.Severity != wantSeverity {
				t.Fatalf("severity = %s, want %s", event.Severity, wantSeverity)
			}
			if !strings.HasSuffix(event.Key, tc.threshold) || !strings.Contains(event.Title, tc.percentage) || event.Message != "Weekly is "+tc.percentage+" used." {
				t.Fatalf("wrong threshold or reported usage: %+v", event)
			}
			if strings.Contains(event.Title, "limit reached") != tc.reached || strings.Contains(event.Title, "nearing limit") == tc.reached {
				t.Fatalf("incorrect limit status: %s", event.Title)
			}
		})
	}
}

func TestAccountMonitorThresholdWindows(t *testing.T) {
	state := monitorTestState()
	now := time.Now()
	pct := 81.0
	window := "2028-01-01T00:00:00Z"
	limits := []models.AccountLimitView{{LimitKey: "five_hour", Label: "Five hour", UsedPercent: &pct, ResetsAt: &window}}
	a := evaluateAccountMonitor(&state, limits, nil, now, "OpenAI")
	if len(a) != 1 {
		t.Fatal(a)
	}
	pct = 96
	b := evaluateAccountMonitor(&state, limits, nil, now, "OpenAI")
	if len(b) != 1 || a[0].Key == b[0].Key {
		t.Fatal(b)
	}
	pct = 85
	c := evaluateAccountMonitor(&state, limits, nil, now, "OpenAI")
	if c[0].Key != b[0].Key {
		t.Fatal("regression must not emit a lower threshold", c)
	}
	pct = 100
	reached := evaluateAccountMonitor(&state, limits, nil, now, "OpenAI")
	if len(reached) != 1 || reached[0].Key == b[0].Key {
		t.Fatal("reaching 100% must generate its own alert", reached)
	}
	repeated := evaluateAccountMonitor(&state, limits, nil, now, "OpenAI")
	if len(repeated) != 1 || repeated[0].Key != reached[0].Key {
		t.Fatal("repeated 100% checks must preserve the deduplication key", repeated)
	}
	window = "2028-01-02T00:00:00Z"
	d := evaluateAccountMonitor(&state, limits, nil, now, "OpenAI")
	if d[0].Key == reached[0].Key {
		t.Fatal("new window must alert again")
	}
}
func TestAccountMonitorResetBaselineExpiryAndGrants(t *testing.T) {
	state := monitorTestState()
	now := time.Now().UTC()
	credit := map[string]any{"id": "one", "status": "available", "remaining_uses": 1, "expires_at": now.Add(23 * time.Hour).Format(time.RFC3339)}
	resets := map[string]any{"credits": []any{credit}}
	a := evaluateAccountMonitor(&state, nil, resets, now, "Anthropic")
	if len(a) != 1 {
		t.Fatal("baseline only expiration", a)
	}
	b := evaluateAccountMonitor(&state, nil, resets, now.Add(22*time.Hour), "Anthropic")
	if len(b) != 1 || a[0].Key == b[0].Key {
		t.Fatal("2-hour reminder", b)
	}
	credit["remaining_uses"] = 2
	c := evaluateAccountMonitor(&state, nil, resets, now, "Anthropic")
	if len(c) != 2 {
		t.Fatal("grant increase and expiry", c)
	}
	d := evaluateAccountMonitor(&state, nil, resets, now.Add(24*time.Hour), "Anthropic")
	if len(d) != 0 {
		t.Fatal("expired reset", d)
	}
	untouched := monitorTestState()
	evaluateAccountMonitor(&untouched, nil, nil, now, "OpenAI")
	if untouched.ResetsInitialized {
		t.Fatal("failed fetch established baseline")
	}
}
func TestAccountMonitorAlertIdempotency(t *testing.T) {
	for _, severity := range []models.AlertSeverity{models.SeverityError, models.SeverityWarning, models.SeverityInfo} {
		t.Run(string(severity), func(t *testing.T) {
			env := teststores.New(t)
			ctx := context.Background()
			projects, err := env.Stores.Projects.List(ctx)
			if err != nil || len(projects) == 0 {
				t.Fatal(projects, err)
			}
			broadcaster := events.NewBroadcaster()
			sub, err := broadcaster.Subscribe()
			if err != nil {
				t.Fatal(err)
			}
			defer broadcaster.Unsubscribe(sub)
			svc := NewAlertService(env.Stores.Alerts, broadcaster)
			for i := 0; i < 2; i++ {
				a := &models.Alert{ProjectID: projects[0].ID, Type: models.AlertCustom, Severity: severity, Source: "account_usage", Title: "Usage", IdempotencyKey: "account-usage:test"}
				if err := svc.CreateAccountUsageAlert(ctx, a, "/analytics?view=usage"); err != nil {
					t.Fatal(err)
				}
			}
			stored, err := env.Stores.Alerts.GetByIdempotencyKey(ctx, projects[0].ID, "account-usage:test")
			if err != nil || stored.Severity != severity {
				t.Fatalf("stored alert = %+v, err = %v", stored, err)
			}
			select {
			case event := <-sub:
				encoded, err := json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}
				var payload map[string]any
				if err := json.Unmarshal(encoded, &payload); err != nil {
					t.Fatal(err)
				}
				if payload["alert_severity"] != string(stored.Severity) {
					t.Fatalf("toast severity differs from stored alert: %s", encoded)
				}
			default:
				t.Fatal("missing toast event")
			}
			select {
			case event := <-sub:
				t.Fatal("duplicate toast event", event)
			default:
			}
		})
	}
}

func TestAccountMonitorPersistsAcrossRuns(t *testing.T) {
	env := teststores.New(t)
	ctx := context.Background()
	projects, _ := env.Stores.Projects.List(ctx)
	cfg := models.LLMConfig{Name: "Test", Provider: models.ProviderOpenAI, Model: "gpt-6.1-sol", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "fixture", OAuthAccountID: "fixture-account", OAuthExpiresAt: time.Now().Add(4 * time.Hour).UnixMilli()}
	if err := env.LLMConfigs.Create(ctx, &cfg); err != nil {
		t.Fatal(err)
	}
	alias := cfg
	alias.ID = ""
	alias.Name = "Another model on the same account"
	if err := env.LLMConfigs.Create(ctx, &alias); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("monitor must not redeem")
		}
		w.Write([]byte(`{"available_count":0,"credits":[]}`))
	}))
	defer server.Close()
	old := openaiclient.OpenAIChatGPTAPIBaseURL
	openaiclient.OpenAIChatGPTAPIBaseURL = server.URL + "/backend-api/codex/"
	defer func() { openaiclient.OpenAIChatGPTAPIBaseURL = old }()
	usage := NewUsageAnalyticsService(env.Usage, env.LLMConfigs)
	pct := 96.0
	windowMinutes := 300
	reset := time.Now().Add(time.Hour).Format(time.RFC3339)
	usage.SetAccountUsageFetcher(func(ctx context.Context, c models.LLMConfig) (*models.AccountUsageResult, error) {
		return &models.AccountUsageResult{Provider: "openai", AccountID: c.OAuthAccountID, AgentConfigID: c.ID, AccountDisplayName: "owner@example.com", PlanType: "ChatGPT Pro", PrimaryLabel: "Session", PrimaryWindowMinutes: &windowMinutes, PrimaryUsedPercent: &pct, PrimaryResetsAt: &reset}, nil
	})
	broadcaster := events.NewBroadcaster()
	sub, err := broadcaster.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer broadcaster.Unsubscribe(sub)
	alerts := NewAlertService(env.Stores.Alerts, broadcaster)
	for i := 0; i < 2; i++ {
		monitor := NewAccountUsageMonitor(usage, env.Stores.Settings, env.Stores.Projects, alerts)
		if err := monitor.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case event := <-sub:
		if event.AlertSource != "account_usage" || event.ProjectID != projects[0].ID || !strings.HasPrefix(event.AlertURL, "/analytics?") {
			t.Fatal(event)
		}
		wantTitle := "OpenAI (owner@example.com · ChatGPT Pro) — 5-hour session: 96% used (nearing limit)"
		if event.AlertTitle != wantTitle {
			t.Fatalf("toast title = %q, want %q", event.AlertTitle, wantTitle)
		}
		stored, err := env.Stores.Alerts.GetByIDForProject(ctx, event.ProjectID, event.AlertID)
		if err != nil || stored.Title != wantTitle {
			t.Fatalf("stored alert = %+v, err = %v", stored, err)
		}
	default:
		t.Fatal("missing toast event")
	}
	select {
	case event := <-sub:
		t.Fatal("duplicate event", event)
	default:
	}
	pct = 100
	if err := NewAccountUsageMonitor(usage, env.Stores.Settings, env.Stores.Projects, alerts).RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-sub:
		if !strings.HasSuffix(event.AlertTitle, "5-hour session: limit reached (100% used)") {
			t.Fatalf("missing actual limit-reached title: %+v", event)
		}
	default:
		t.Fatal("100% must alert even after the 95% warning")
	}
	if err := NewAccountUsageMonitor(usage, env.Stores.Settings, env.Stores.Projects, alerts).RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-sub:
		t.Fatal("duplicate 100% alert", event)
	default:
	}
	if err := env.Stores.Settings.Set(ctx, "account_usage_alerts:"+projects[0].ID, "false"); err != nil {
		t.Fatal(err)
	}
	reset = time.Now().Add(2 * time.Hour).Format(time.RFC3339)
	if err := NewAccountUsageMonitor(usage, env.Stores.Settings, env.Stores.Projects, alerts).RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sub:
	default:
		t.Fatal("automatic monitoring must ignore the removed preference")
	}
}

func TestAccountReadSharingAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc := NewUsageAnalyticsService(nil, nil)
		ctx, cancel := context.WithCancel(context.Background())
		release := make(chan struct{})
		calls := 0
		read := func(work context.Context) (any, error) {
			calls++
			select {
			case <-release:
				return "ok", nil
			case <-work.Done():
				return nil, work.Err()
			}
		}
		first := make(chan error, 1)
		second := make(chan error, 1)
		go func() { _, err := svc.sharedAccountRead(ctx, "same-account", read); first <- err }()
		synctest.Wait()
		go func() {
			value, err := svc.sharedAccountRead(context.Background(), "same-account", read)
			if err == nil && value != "ok" {
				err = fmt.Errorf("unexpected value %v", value)
			}
			second <- err
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
		if err := <-first; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		close(release)
		synctest.Wait()
		if err := <-second; err != nil {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatalf("shared read made %d requests", calls)
		}
		if _, err := svc.sharedAccountRead(context.Background(), "same-account", read); err != nil {
			t.Fatal(err)
		}
		if calls != 2 {
			t.Fatal("completed result cached unexpectedly")
		}
	})
}
