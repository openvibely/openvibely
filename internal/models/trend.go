package models

import (
	"encoding/json"
	"time"
)

// XCredentials stores X (Twitter) API credentials per project.
type XCredentials struct {
	ID                string    `json:"id"`
	ProjectID         string    `json:"project_id"`
	APIKey            string    `json:"api_key"`
	APISecret         string    `json:"api_secret"`
	AccessToken       string    `json:"access_token"`
	AccessTokenSecret string    `json:"access_token_secret"`
	BearerToken       string    `json:"bearer_token"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// HasCredentials returns true if at least bearer token or API key+secret are set.
func (x *XCredentials) HasCredentials() bool {
	return x.BearerToken != "" || (x.APIKey != "" && x.APISecret != "")
}

// TrendSourceType defines the type of trend source.
type TrendSourceType string

const (
	TrendSourceHashtag    TrendSourceType = "hashtag"
	TrendSourceAccount    TrendSourceType = "account"
	TrendSourceKeyword    TrendSourceType = "keyword"
	TrendSourceCompetitor TrendSourceType = "competitor"
)

// TrendSource represents a monitored source (hashtag, account, keyword, competitor).
type TrendSource struct {
	ID         string          `json:"id"`
	ProjectID  string          `json:"project_id"`
	SourceType TrendSourceType `json:"source_type"`
	Value      string          `json:"value"`
	Enabled    bool            `json:"enabled"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// TrendSentiment defines the sentiment of a trend entry.
type TrendSentiment string

const (
	SentimentPositive TrendSentiment = "positive"
	SentimentNegative TrendSentiment = "negative"
	SentimentNeutral  TrendSentiment = "neutral"
	SentimentMixed    TrendSentiment = "mixed"
)

// TrendEntry represents a collected piece of trend data (e.g., a tweet or post).
type TrendEntry struct {
	ID              string         `json:"id"`
	ProjectID       string         `json:"project_id"`
	SourceID        *string        `json:"source_id,omitempty"`
	SourceType      string         `json:"source_type"`
	Content         string         `json:"content"`
	Author          string         `json:"author"`
	URL             string         `json:"url"`
	EngagementScore int            `json:"engagement_score"`
	Sentiment       TrendSentiment `json:"sentiment"`
	CollectedAt     time.Time      `json:"collected_at"`
	RawData         string         `json:"raw_data"`
}

// TrendPatternType defines the type of identified pattern.
type TrendPatternType string

const (
	PatternFeatureRequest TrendPatternType = "feature_request"
	PatternPainPoint      TrendPatternType = "pain_point"
	PatternEmergingTech   TrendPatternType = "emerging_tech"
	PatternMarketShift    TrendPatternType = "market_shift"
	PatternCompetitorMove TrendPatternType = "competitor_move"
	PatternUserSentiment  TrendPatternType = "user_sentiment"
)

// TrendPatternStatus defines the status of a trend pattern.
type TrendPatternStatus string

const (
	PatternStatusActive      TrendPatternStatus = "active"
	PatternStatusImplemented TrendPatternStatus = "implemented"
	PatternStatusDismissed   TrendPatternStatus = "dismissed"
	PatternStatusStale       TrendPatternStatus = "stale"
)

// TrendPattern represents an identified pattern from trend analysis.
type TrendPattern struct {
	ID           string             `json:"id"`
	ProjectID    string             `json:"project_id"`
	PatternType  TrendPatternType   `json:"pattern_type"`
	Title        string             `json:"title"`
	Description  string             `json:"description"`
	Evidence     string             `json:"evidence"` // JSON array of evidence strings
	Confidence   float64            `json:"confidence"`
	SignalCount  int                `json:"signal_count"`
	FirstSeen    time.Time          `json:"first_seen"`
	LastSeen     time.Time          `json:"last_seen"`
	Status       TrendPatternStatus `json:"status"`
	LedToFeature string             `json:"led_to_feature"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
}

// ParseEvidence returns the list of evidence strings.
func (p *TrendPattern) ParseEvidence() ([]string, error) {
	if p.Evidence == "" || p.Evidence == "[]" {
		return nil, nil
	}
	var evidence []string
	if err := json.Unmarshal([]byte(p.Evidence), &evidence); err != nil {
		return nil, err
	}
	return evidence, nil
}

// CompetitorUpdateType defines the type of competitor update.
type CompetitorUpdateType string

const (
	CompUpdateFeatureLaunch CompetitorUpdateType = "feature_launch"
	CompUpdateChangelog     CompetitorUpdateType = "changelog"
	CompUpdatePricing       CompetitorUpdateType = "pricing_change"
	CompUpdateAcquisition   CompetitorUpdateType = "acquisition"
	CompUpdatePartnership   CompetitorUpdateType = "partnership"
	CompUpdateUserFeedback  CompetitorUpdateType = "user_feedback"
)

// CompetitorUpdate represents a tracked competitor product update.
type CompetitorUpdate struct {
	ID                string               `json:"id"`
	ProjectID         string               `json:"project_id"`
	CompetitorName    string               `json:"competitor_name"`
	UpdateType        CompetitorUpdateType `json:"update_type"`
	Title             string               `json:"title"`
	Description       string               `json:"description"`
	SourceURL         string               `json:"source_url"`
	ImpactAssessment  string               `json:"impact_assessment"`
	RelevanceScore    float64              `json:"relevance_score"`
	DetectedAt        time.Time            `json:"detected_at"`
}

// TrendDashboardData holds all data for the trend intelligence section of the autonomous dashboard.
type TrendDashboardData struct {
	Sources          []TrendSource       `json:"sources"`
	RecentEntries    []TrendEntry        `json:"recent_entries"`
	ActivePatterns   []TrendPattern      `json:"active_patterns"`
	CompetitorNews   []CompetitorUpdate  `json:"competitor_news"`
	HasXCredentials  bool                `json:"has_x_credentials"`
	Stats            TrendStats          `json:"stats"`
}

// TrendStats contains summary statistics about collected trends.
type TrendStats struct {
	TotalEntries       int `json:"total_entries"`
	ActivePatterns     int `json:"active_patterns"`
	CompetitorUpdates  int `json:"competitor_updates"`
	ImplementedCount   int `json:"implemented_count"`
	MonitoredSources   int `json:"monitored_sources"`
}
