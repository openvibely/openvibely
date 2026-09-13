package models

import "time"

// LLMUsageEvent records one completed provider model call for analytics.
type LLMUsageEvent struct {
	ID                       string    `json:"id"`
	Provider                 string    `json:"provider"`
	AccountID                string    `json:"account_id,omitempty"`
	ProjectID                string    `json:"project_id,omitempty"`
	TaskID                   string    `json:"task_id,omitempty"`
	ExecutionID              string    `json:"execution_id,omitempty"`
	ChatThreadID             string    `json:"chat_thread_id,omitempty"`
	TurnID                   string    `json:"turn_id,omitempty"`
	AgentConfigID            string    `json:"agent_config_id,omitempty"`
	Model                    string    `json:"model"`
	Operation                string    `json:"operation"`
	Status                   string    `json:"status"`
	ErrorMessage             string    `json:"error_message,omitempty"`
	InputTokens              int       `json:"input_tokens"`
	OutputTokens             int       `json:"output_tokens"`
	CachedInputTokens        int       `json:"cached_input_tokens"`
	CacheCreationInputTokens int       `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int       `json:"cache_read_input_tokens"`
	ReasoningOutputTokens    int       `json:"reasoning_output_tokens"`
	TotalTokens              int       `json:"total_tokens"`
	CostUSD                  *float64  `json:"cost_usd,omitempty"`
	LatencyMs                *int64    `json:"latency_ms,omitempty"`
	ContextWindow            *int      `json:"context_window,omitempty"`
	MaxOutputTokens          *int      `json:"max_output_tokens,omitempty"`
	ProviderResponseID       string    `json:"provider_response_id,omitempty"`
	RawUsageJSON             string    `json:"raw_usage_json"`
	OccurredAt               time.Time `json:"occurred_at"`
	CreatedAt                time.Time `json:"created_at"`
}

// AccountUsageSnapshot stores live OAuth account limit data fetched for Analytics.
type AccountUsageSnapshot struct {
	ID                      string                   `json:"id"`
	Provider                string                   `json:"provider"`
	AccountID               string                   `json:"account_id,omitempty"`
	AgentConfigID           string                   `json:"agent_config_id,omitempty"`
	OAuthConfigRevision     int64                    `json:"-"`
	PlanType                string                   `json:"plan_type,omitempty"`
	AccountDisplayName      string                   `json:"account_display_name,omitempty"`
	AccountDetail           string                   `json:"account_detail,omitempty"`
	BillingLabel            string                   `json:"billing_label,omitempty"`
	SubscriptionStatus      string                   `json:"subscription_status,omitempty"`
	ExtraUsageLabel         string                   `json:"extra_usage_label,omitempty"`
	ExtraUsageMonthlyUSD    *float64                 `json:"extra_usage_monthly_usd,omitempty"`
	ExtraUsageUsedUSD       *float64                 `json:"extra_usage_used_usd,omitempty"`
	CreditsRemaining        *float64                 `json:"credits_remaining,omitempty"`
	PrimaryLabel            string                   `json:"primary_label,omitempty"`
	PrimaryUsedPercent      *float64                 `json:"primary_used_percent,omitempty"`
	PrimaryWindowMinutes    *int                     `json:"primary_window_minutes,omitempty"`
	PrimaryResetsAt         *string                  `json:"primary_resets_at,omitempty"`
	SecondaryLabel          string                   `json:"secondary_label,omitempty"`
	SecondaryUsedPercent    *float64                 `json:"secondary_used_percent,omitempty"`
	SecondaryWindowMinutes  *int                     `json:"secondary_window_minutes,omitempty"`
	SecondaryResetsAt       *string                  `json:"secondary_resets_at,omitempty"`
	ModelLimitLabel         string                   `json:"model_limit_label,omitempty"`
	ModelLimitUsedPercent   *float64                 `json:"model_limit_used_percent,omitempty"`
	ModelLimitWindowMinutes *int                     `json:"model_limit_window_minutes,omitempty"`
	ModelLimitResetsAt      *string                  `json:"model_limit_resets_at,omitempty"`
	ExtraLimits             []AccountUsageExtraLimit `json:"extra_limits,omitempty"`
	RateLimitReachedType    string                   `json:"rate_limit_reached_type,omitempty"`
	RawJSON                 string                   `json:"raw_json"`
	FetchedAt               time.Time                `json:"fetched_at"`
	CreatedAt               time.Time                `json:"created_at"`
}

// AccountUsageExtraLimit stores provider-specific account limit rows beyond primary/secondary windows.
type AccountUsageExtraLimit struct {
	ID            string   `json:"id,omitempty"`
	SnapshotID    string   `json:"snapshot_id,omitempty"`
	Provider      string   `json:"provider"`
	AccountID     string   `json:"account_id,omitempty"`
	AgentConfigID string   `json:"agent_config_id,omitempty"`
	LimitKey      string   `json:"limit_key"`
	Label         string   `json:"label"`
	UsedPercent   *float64 `json:"used_percent,omitempty"`
	WindowMinutes *int     `json:"window_minutes,omitempty"`
	ResetAt       *string  `json:"reset_at,omitempty"`
	RawJSON       string   `json:"raw_json"`
}

type UsageTotals struct {
	InputTokens              int      `json:"input_tokens"`
	OutputTokens             int      `json:"output_tokens"`
	CachedInputTokens        int      `json:"cached_input_tokens"`
	CacheCreationInputTokens int      `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int      `json:"cache_read_input_tokens"`
	ReasoningOutputTokens    int      `json:"reasoning_output_tokens"`
	TotalTokens              int      `json:"total_tokens"`
	CostUSD                  *float64 `json:"cost_usd,omitempty"`
	CostAvailable            bool     `json:"cost_available"`
	CallCount                int      `json:"call_count"`
}

type DailyUsagePoint struct {
	Period       string   `json:"period"`
	Provider     string   `json:"provider,omitempty"`
	Model        string   `json:"model,omitempty"`
	InputTokens  int      `json:"input_tokens"`
	OutputTokens int      `json:"output_tokens"`
	CacheTokens  int      `json:"cached_input_tokens"`
	TotalTokens  int      `json:"total_tokens"`
	CostUSD      *float64 `json:"cost_usd,omitempty"`
	CallCount    int      `json:"call_count"`
}

type UsageRatePoint struct {
	Period      string `json:"period"`
	Provider    string `json:"provider,omitempty"`
	Model       string `json:"model,omitempty"`
	TotalTokens int    `json:"total_tokens"`
	CallCount   int    `json:"call_count"`
}

type ModelUsagePoint struct {
	Provider              string   `json:"provider"`
	Model                 string   `json:"model"`
	TotalTokens           int      `json:"total_tokens"`
	InputTokens           int      `json:"input_tokens"`
	OutputTokens          int      `json:"output_tokens"`
	CacheTokens           int      `json:"cached_input_tokens"`
	ReasoningOutputTokens int      `json:"reasoning_output_tokens"`
	CostUSD               *float64 `json:"cost_usd,omitempty"`
	CallCount             int      `json:"call_count"`
	Percent               float64  `json:"percent"`
}

type AccountLimitView struct {
	LimitKey      string   `json:"limit_key,omitempty"`
	Label         string   `json:"label"`
	UsedPercent   *float64 `json:"used_percent,omitempty"`
	WindowMinutes *int     `json:"window_minutes,omitempty"`
	ResetsAt      *string  `json:"resets_at,omitempty"`
	Status        string   `json:"status"`
}

type AccountUsageView struct {
	Provider             string             `json:"provider"`
	AccountID            string             `json:"-"`
	AgentConfigID        string             `json:"-"`
	PlanType             string             `json:"plan_type,omitempty"`
	AccountDetail        string             `json:"account_detail,omitempty"`
	BillingLabel         string             `json:"billing_label,omitempty"`
	StatusLabel          string             `json:"status_label,omitempty"`
	ExtraUsageLabel      string             `json:"extra_usage_label,omitempty"`
	ExtraUsageMonthlyUSD *float64           `json:"extra_usage_monthly_usd,omitempty"`
	ExtraUsageUsedUSD    *float64           `json:"extra_usage_used_usd,omitempty"`
	UpdatedAt            time.Time          `json:"updated_at"`
	PrimaryLimit         *AccountLimitView  `json:"primary_limit,omitempty"`
	SecondaryLimit       *AccountLimitView  `json:"secondary_limit,omitempty"`
	ExtraLimits          []AccountLimitView `json:"extra_limits,omitempty"`
	Limits               []AccountLimitView `json:"limits"`
	Error                string             `json:"error,omitempty"`
}

type AnalyticsUsageViewModel struct {
	AccountLimits     []AccountUsageView `json:"account_limits"`
	Totals            UsageTotals        `json:"totals"`
	DailyUsage        []DailyUsagePoint  `json:"daily_usage"`
	DailyUsageByModel []DailyUsagePoint  `json:"daily_usage_by_model,omitempty"`
	UsageRate         []UsageRatePoint   `json:"usage_rate"`
	UsageRateByModel  []UsageRatePoint   `json:"usage_rate_by_model,omitempty"`
	ModelBreakdown    []ModelUsagePoint  `json:"model_breakdown"`
	LastUpdatedAt     *time.Time         `json:"last_updated_at,omitempty"`
	Errors            []string           `json:"errors,omitempty"`
}
