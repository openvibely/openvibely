package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/util"
)

// TrendIntelligenceService handles external trend collection, analysis, and pattern recognition.
type TrendIntelligenceService struct {
	trendRepo     *repository.TrendRepo
	projectRepo   *repository.ProjectRepo
	llmConfigRepo *repository.LLMConfigRepo
	llmSvc        *LLMService
	httpClient    *http.Client
}

func NewTrendIntelligenceService(
	trendRepo *repository.TrendRepo,
	projectRepo *repository.ProjectRepo,
	llmConfigRepo *repository.LLMConfigRepo,
) *TrendIntelligenceService {
	return &TrendIntelligenceService{
		trendRepo:     trendRepo,
		projectRepo:   projectRepo,
		llmConfigRepo: llmConfigRepo,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// SetLLMService sets the LLM service for AI analysis calls.
// Called after construction to avoid circular dependencies.
func (s *TrendIntelligenceService) SetLLMService(llmSvc *LLMService) {
	s.llmSvc = llmSvc
}

// GetDashboardData returns all trend intelligence data for the dashboard.
func (s *TrendIntelligenceService) GetDashboardData(ctx context.Context, projectID string) (*models.TrendDashboardData, error) {
	var (
		sources          []models.TrendSource
		entries          []models.TrendEntry
		patterns         []models.TrendPattern
		compUpdates      []models.CompetitorUpdate
		creds            *models.XCredentials
		totalEntries     int
		activePatterns   int
		compCount        int
		implementedCount int
		sourceCount      int
	)

	var g errgroup.Group

	g.Go(func() error {
		var err error
		sources, err = s.trendRepo.ListSources(ctx, projectID)
		if err != nil {
			return fmt.Errorf("list sources: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		var err error
		entries, err = s.trendRepo.ListRecentEntries(ctx, projectID, 20)
		if err != nil {
			return fmt.Errorf("list entries: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		var err error
		patterns, err = s.trendRepo.ListActivePatterns(ctx, projectID)
		if err != nil {
			return fmt.Errorf("list patterns: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		var err error
		compUpdates, err = s.trendRepo.ListRecentCompetitorUpdates(ctx, projectID, 10)
		if err != nil {
			return fmt.Errorf("list competitor updates: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		creds, _ = s.trendRepo.GetXCredentials(ctx, projectID)
		return nil
	})

	g.Go(func() error {
		totalEntries, _ = s.trendRepo.CountEntries(ctx, projectID)
		return nil
	})

	g.Go(func() error {
		activePatterns, _ = s.trendRepo.CountActivePatterns(ctx, projectID)
		return nil
	})

	g.Go(func() error {
		compCount, _ = s.trendRepo.CountCompetitorUpdates(ctx, projectID)
		return nil
	})

	g.Go(func() error {
		implementedCount, _ = s.trendRepo.CountImplementedPatterns(ctx, projectID)
		return nil
	})

	g.Go(func() error {
		sourceCount, _ = s.trendRepo.CountSources(ctx, projectID)
		return nil
	})

	if err := g.Wait(); err != nil {
		return nil, err
	}

	hasCreds := creds != nil && creds.HasCredentials()

	return &models.TrendDashboardData{
		Sources:         sources,
		RecentEntries:   entries,
		ActivePatterns:  patterns,
		CompetitorNews:  compUpdates,
		HasXCredentials: hasCreds,
		Stats: models.TrendStats{
			TotalEntries:      totalEntries,
			ActivePatterns:    activePatterns,
			CompetitorUpdates: compCount,
			ImplementedCount:  implementedCount,
			MonitoredSources:  sourceCount,
		},
	}, nil
}

// CollectFromX fetches recent posts from X (Twitter) based on configured sources.
func (s *TrendIntelligenceService) CollectFromX(ctx context.Context, projectID string) (int, error) {
	creds, err := s.trendRepo.GetXCredentials(ctx, projectID)
	if err != nil {
		return 0, fmt.Errorf("get X credentials: %w", err)
	}
	if creds == nil || !creds.HasCredentials() {
		return 0, fmt.Errorf("X API credentials not configured for this project")
	}

	sources, err := s.trendRepo.ListEnabledSources(ctx, projectID)
	if err != nil {
		return 0, fmt.Errorf("list sources: %w", err)
	}
	if len(sources) == 0 {
		return 0, fmt.Errorf("no enabled trend sources configured")
	}

	collected := 0
	for _, source := range sources {
		count, err := s.collectFromXForSource(ctx, projectID, creds, &source)
		if err != nil {
			log.Printf("[trend-intelligence] error collecting from X for source %s: %v", source.Value, err)
			continue
		}
		collected += count
	}

	log.Printf("[trend-intelligence] collected %d entries from X for project=%s", collected, projectID)
	return collected, nil
}

// collectFromXForSource fetches posts from X for a single source.
func (s *TrendIntelligenceService) collectFromXForSource(ctx context.Context, projectID string, creds *models.XCredentials, source *models.TrendSource) (int, error) {
	query := buildXSearchQuery(source)
	if query == "" {
		return 0, nil
	}

	tweets, err := s.searchXTweets(ctx, creds, query)
	if err != nil {
		return 0, fmt.Errorf("search X: %w", err)
	}

	collected := 0
	for _, tweet := range tweets {
		entry := &models.TrendEntry{
			ProjectID:       projectID,
			SourceID:        &source.ID,
			SourceType:      string(source.SourceType),
			Content:         tweet.Text,
			Author:          tweet.AuthorID,
			URL:             fmt.Sprintf("https://x.com/i/status/%s", tweet.ID),
			EngagementScore: tweet.PublicMetrics.LikeCount + tweet.PublicMetrics.RetweetCount + tweet.PublicMetrics.ReplyCount,
			Sentiment:       models.SentimentNeutral, // Will be analyzed later
			RawData:         "{}",
		}
		if raw, err := json.Marshal(tweet); err == nil {
			entry.RawData = string(raw)
		}

		if err := s.trendRepo.CreateEntry(ctx, entry); err != nil {
			log.Printf("[trend-intelligence] error saving entry: %v", err)
			continue
		}
		collected++
	}
	return collected, nil
}

// xTweet represents a tweet from X API v2.
type xTweet struct {
	ID            string       `json:"id"`
	Text          string       `json:"text"`
	AuthorID      string       `json:"author_id"`
	CreatedAt     string       `json:"created_at"`
	PublicMetrics xMetrics     `json:"public_metrics"`
}

type xMetrics struct {
	RetweetCount int `json:"retweet_count"`
	ReplyCount   int `json:"reply_count"`
	LikeCount    int `json:"like_count"`
	QuoteCount   int `json:"quote_count"`
}

type xSearchResponse struct {
	Data []xTweet `json:"data"`
	Meta struct {
		ResultCount int    `json:"result_count"`
		NextToken   string `json:"next_token"`
	} `json:"meta"`
}

// searchXTweets calls X API v2 recent search endpoint.
func (s *TrendIntelligenceService) searchXTweets(ctx context.Context, creds *models.XCredentials, query string) ([]xTweet, error) {
	endpoint := "https://api.twitter.com/2/tweets/search/recent"
	params := url.Values{}
	params.Set("query", query)
	params.Set("max_results", "25")
	params.Set("tweet.fields", "author_id,created_at,public_metrics")

	reqURL := fmt.Sprintf("%s?%s", endpoint, params.Encode())
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Use bearer token for app-only authentication
	if creds.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+creds.BearerToken)
	} else {
		return nil, fmt.Errorf("bearer token required for X API v2 search")
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("X API request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("X API returned status %d: %s", resp.StatusCode, string(body))
	}

	var searchResp xSearchResponse
	if err := json.Unmarshal(body, &searchResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return searchResp.Data, nil
}

// buildXSearchQuery constructs an X API search query from a trend source.
func buildXSearchQuery(source *models.TrendSource) string {
	switch source.SourceType {
	case models.TrendSourceHashtag:
		tag := source.Value
		if !strings.HasPrefix(tag, "#") {
			tag = "#" + tag
		}
		return tag + " -is:retweet lang:en"
	case models.TrendSourceAccount:
		acct := source.Value
		if !strings.HasPrefix(acct, "@") {
			acct = "@" + acct
		}
		return "from:" + strings.TrimPrefix(acct, "@") + " -is:retweet"
	case models.TrendSourceKeyword:
		return source.Value + " -is:retweet lang:en"
	case models.TrendSourceCompetitor:
		return source.Value + " -is:retweet lang:en"
	default:
		return ""
	}
}

// AnalyzeTrends uses AI to analyze collected trend entries and identify patterns.
func (s *TrendIntelligenceService) AnalyzeTrends(ctx context.Context, projectID string) ([]models.TrendPattern, error) {
	if s.llmSvc == nil {
		return nil, fmt.Errorf("LLM service not available")
	}

	// Get recent entries to analyze
	entries, err := s.trendRepo.ListRecentEntries(ctx, projectID, 100)
	if err != nil {
		return nil, fmt.Errorf("list entries: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no trend entries to analyze")
	}

	// Get project context
	project, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		return nil, fmt.Errorf("get project: %w", err)
	}

	agent, err := s.llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		return nil, fmt.Errorf("no default agent config")
	}

	// Build analysis prompt
	prompt := s.buildTrendAnalysisPrompt(project, entries)

	response, _, err := s.llmSvc.CallAgentDirect(ctx, prompt, nil, *agent, "")
	if err != nil {
		return nil, fmt.Errorf("AI analysis: %w", err)
	}

	// Parse AI response
	patterns, err := s.parseTrendPatternsResponse(response)
	if err != nil {
		return nil, fmt.Errorf("parse AI response: %w", err)
	}

	// Save patterns, deduplicating against existing ones
	var saved []models.TrendPattern
	for i := range patterns {
		patterns[i].ProjectID = projectID

		existing, _ := s.trendRepo.ExistingPattern(ctx, projectID, patterns[i].Title, patterns[i].PatternType)
		if existing != nil {
			// Bump existing pattern
			_ = s.trendRepo.BumpPattern(ctx, existing.ID, patterns[i].SignalCount)
			saved = append(saved, *existing)
			continue
		}

		if err := s.trendRepo.CreatePattern(ctx, &patterns[i]); err != nil {
			log.Printf("[trend-intelligence] error saving pattern: %v", err)
			continue
		}
		saved = append(saved, patterns[i])
	}

	log.Printf("[trend-intelligence] analyzed trends for project=%s, identified %d patterns", projectID, len(saved))
	return saved, nil
}

// AnalyzeCompetitors uses AI to analyze competitor sources and generate competitive intelligence.
func (s *TrendIntelligenceService) AnalyzeCompetitors(ctx context.Context, projectID string) ([]models.CompetitorUpdate, error) {
	if s.llmSvc == nil {
		return nil, fmt.Errorf("LLM service not available")
	}

	// Get competitor sources
	sources, err := s.trendRepo.ListEnabledSources(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}

	var competitorSources []models.TrendSource
	for _, src := range sources {
		if src.SourceType == models.TrendSourceCompetitor {
			competitorSources = append(competitorSources, src)
		}
	}

	// Also get recent entries that mention competitors
	entries, err := s.trendRepo.ListRecentEntries(ctx, projectID, 50)
	if err != nil {
		return nil, fmt.Errorf("list entries: %w", err)
	}

	project, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		return nil, fmt.Errorf("get project: %w", err)
	}

	agent, err := s.llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		return nil, fmt.Errorf("no default agent config")
	}

	prompt := s.buildCompetitorAnalysisPrompt(project, competitorSources, entries)

	response, _, err := s.llmSvc.CallAgentDirect(ctx, prompt, nil, *agent, "")
	if err != nil {
		return nil, fmt.Errorf("AI analysis: %w", err)
	}

	updates, err := s.parseCompetitorUpdatesResponse(response)
	if err != nil {
		return nil, fmt.Errorf("parse AI response: %w", err)
	}

	var saved []models.CompetitorUpdate
	for i := range updates {
		updates[i].ProjectID = projectID
		if err := s.trendRepo.CreateCompetitorUpdate(ctx, &updates[i]); err != nil {
			log.Printf("[trend-intelligence] error saving competitor update: %v", err)
			continue
		}
		saved = append(saved, updates[i])
	}

	log.Printf("[trend-intelligence] analyzed competitors for project=%s, found %d updates", projectID, len(saved))
	return saved, nil
}

// GetTrendContext returns a formatted string of current trend intelligence
// suitable for inclusion in the autonomous discovery prompt.
func (s *TrendIntelligenceService) GetTrendContext(ctx context.Context, projectID string) string {
	var sb strings.Builder

	patterns, _ := s.trendRepo.ListActivePatterns(ctx, projectID)
	compUpdates, _ := s.trendRepo.ListRecentCompetitorUpdates(ctx, projectID, 10)
	entries, _ := s.trendRepo.ListRecentEntries(ctx, projectID, 20)

	if len(patterns) == 0 && len(compUpdates) == 0 && len(entries) == 0 {
		return ""
	}

	sb.WriteString("\n## External Trend Intelligence\n\n")

	if len(patterns) > 0 {
		sb.WriteString("### Identified Patterns\n")
		for _, p := range patterns {
			sb.WriteString(fmt.Sprintf("- **%s** [%s] (confidence: %.0f%%, signals: %d): %s\n",
				p.Title, string(p.PatternType), p.Confidence*100, p.SignalCount, p.Description))
		}
		sb.WriteString("\n")
	}

	if len(compUpdates) > 0 {
		sb.WriteString("### Competitor Intelligence\n")
		for _, u := range compUpdates {
			sb.WriteString(fmt.Sprintf("- **%s** (%s): %s - %s\n",
				u.CompetitorName, string(u.UpdateType), u.Title, u.Description))
		}
		sb.WriteString("\n")
	}

	if len(entries) > 0 {
		sb.WriteString("### Recent Market Signals\n")
		limit := 10
		if len(entries) < limit {
			limit = len(entries)
		}
		for _, e := range entries[:limit] {
			content := e.Content
			if len(content) > 200 {
				content = content[:200] + "..."
			}
			sb.WriteString(fmt.Sprintf("- [%s] %s (engagement: %d)\n",
				e.SourceType, content, e.EngagementScore))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("### Instructions for Using Trend Data\n")
	sb.WriteString("- Reference specific trends when proposing features (e.g., 'Based on increased discussion about X...')\n")
	sb.WriteString("- Prioritize features that address identified pain points from real user signals\n")
	sb.WriteString("- Consider competitive gaps revealed by competitor intelligence\n")
	sb.WriteString("- Factor in engagement scores to gauge demand intensity\n\n")

	return sb.String()
}

// GetCostAnalysisContext returns a formatted string of cost/scale considerations
// for the feature selection phase.
func (s *TrendIntelligenceService) GetCostAnalysisContext() string {
	return `
### Cost & Scale Analysis Criteria
When evaluating features, also consider:
- **API Cost Impact**: Will this feature increase LLM API usage? Estimate cost per user per month.
- **Infrastructure Cost**: Does this need additional storage, compute, or external services?
- **Operational Complexity**: How much ongoing maintenance is needed?
- **Scalability**: Will this feature work well with 10x the current usage?
- **Value-to-Cost Ratio**: High value, low cost features should be prioritized.

Add these fields to your evaluation:
- estimated_monthly_cost: "low" (<$5/user), "medium" ($5-50/user), "high" (>$50/user)
- scalability_risk: "low", "medium", "high"
- value_to_cost_ratio: 0.0 to 1.0 (higher = better value per dollar)
`
}

// RecordFeatureOutcome tracks that a trend pattern led to a successful feature.
func (s *TrendIntelligenceService) RecordFeatureOutcome(ctx context.Context, patternID, featureName string) error {
	return s.trendRepo.UpdatePatternStatus(ctx, patternID, models.PatternStatusImplemented, featureName)
}

// DismissPattern marks a trend pattern as dismissed.
func (s *TrendIntelligenceService) DismissPattern(ctx context.Context, patternID string) error {
	return s.trendRepo.UpdatePatternStatus(ctx, patternID, models.PatternStatusDismissed, "")
}

// --- Credential Management ---

// GetXCredentials returns X API credentials for a project.
func (s *TrendIntelligenceService) GetXCredentials(ctx context.Context, projectID string) (*models.XCredentials, error) {
	return s.trendRepo.GetXCredentials(ctx, projectID)
}

// SaveXCredentials saves or updates X API credentials.
func (s *TrendIntelligenceService) SaveXCredentials(ctx context.Context, creds *models.XCredentials) error {
	return s.trendRepo.UpsertXCredentials(ctx, creds)
}

// --- Source Management ---

// AddSource adds a new trend monitoring source.
func (s *TrendIntelligenceService) AddSource(ctx context.Context, source *models.TrendSource) error {
	return s.trendRepo.CreateSource(ctx, source)
}

// ListSources returns all trend sources for a project.
func (s *TrendIntelligenceService) ListSources(ctx context.Context, projectID string) ([]models.TrendSource, error) {
	return s.trendRepo.ListSources(ctx, projectID)
}

// DeleteSource removes a trend source.
func (s *TrendIntelligenceService) DeleteSource(ctx context.Context, id string) error {
	return s.trendRepo.DeleteSource(ctx, id)
}

// ToggleSource enables or disables a trend source.
func (s *TrendIntelligenceService) ToggleSource(ctx context.Context, id string, enabled bool) error {
	return s.trendRepo.ToggleSource(ctx, id, enabled)
}

// --- AI Prompt Builders ---

func (s *TrendIntelligenceService) buildTrendAnalysisPrompt(project *models.Project, entries []models.TrendEntry) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`You are analyzing external market signals for a product called "%s" (%s).

Analyze the following collected data points and identify actionable patterns.

## Collected Data Points
`, project.Name, project.Description))

	for i, e := range entries {
		if i >= 50 {
			break
		}
		sb.WriteString(fmt.Sprintf("### Entry %d [%s]\n", i+1, e.SourceType))
		sb.WriteString(fmt.Sprintf("- Author: %s\n", e.Author))
		sb.WriteString(fmt.Sprintf("- Content: %s\n", e.Content))
		sb.WriteString(fmt.Sprintf("- Engagement: %d\n\n", e.EngagementScore))
	}

	sb.WriteString(`## Instructions
Identify recurring patterns, themes, and actionable insights from this data. For each pattern found, classify it as one of:
- feature_request: Users explicitly asking for a capability
- pain_point: Users expressing frustration with current tools
- emerging_tech: New technology or approach gaining traction
- market_shift: Change in how users think about the product category
- competitor_move: Competitor action that creates an opportunity or threat
- user_sentiment: General sentiment trend about the product space

## Output Format
Return a JSON array of identified patterns:
[
  {
    "pattern_type": "feature_request",
    "title": "Short pattern title",
    "description": "2-3 sentence description of the pattern and its implications",
    "evidence": ["quote or reference 1", "quote or reference 2"],
    "confidence": 0.8,
    "signal_count": 5
  }
]

Return ONLY the JSON array, no other text.`)

	return sb.String()
}

func (s *TrendIntelligenceService) buildCompetitorAnalysisPrompt(project *models.Project, competitors []models.TrendSource, entries []models.TrendEntry) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`You are performing competitive intelligence analysis for a product called "%s" (%s).

`, project.Name, project.Description))

	if len(competitors) > 0 {
		sb.WriteString("## Tracked Competitors\n")
		for _, c := range competitors {
			sb.WriteString(fmt.Sprintf("- %s\n", c.Value))
		}
		sb.WriteString("\n")
	}

	if len(entries) > 0 {
		sb.WriteString("## Recent Market Data\n")
		for i, e := range entries {
			if i >= 30 {
				break
			}
			sb.WriteString(fmt.Sprintf("- [%s] %s (engagement: %d)\n", e.SourceType, e.Content, e.EngagementScore))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(`## Instructions
Based on the tracked competitors and market data, identify significant competitor activities and market opportunities.

For each finding, classify the update type as one of:
- feature_launch: A competitor released a new feature
- changelog: Notable product update or improvement
- pricing_change: Changes to pricing model or tiers
- acquisition: Merger, acquisition, or significant partnership
- partnership: Strategic partnership or integration
- user_feedback: Notable user response to competitor actions

## Output Format
Return a JSON array of competitor updates:
[
  {
    "competitor_name": "Competitor Name",
    "update_type": "feature_launch",
    "title": "Short title",
    "description": "What happened and what it means",
    "impact_assessment": "How this affects our product strategy",
    "relevance_score": 0.8
  }
]

Return ONLY the JSON array, no other text.`)

	return sb.String()
}

// --- Response Parsers ---

func (s *TrendIntelligenceService) parseTrendPatternsResponse(response string) ([]models.TrendPattern, error) {
	jsonStr := util.ExtractJSONArray(response)
	if jsonStr == "" {
		// Fall back to object extraction
		jsonStr = util.ExtractJSONObject(response)
	}
	if jsonStr == "" {
		return nil, fmt.Errorf("no JSON found in response")
	}

	// Try parsing as array first
	var rawPatterns []struct {
		PatternType string   `json:"pattern_type"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Evidence    []string `json:"evidence"`
		Confidence  float64  `json:"confidence"`
		SignalCount int      `json:"signal_count"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &rawPatterns); err != nil {
		return nil, fmt.Errorf("parse patterns JSON: %w", err)
	}

	var patterns []models.TrendPattern
	for _, rp := range rawPatterns {
		evidenceJSON := "[]"
		if len(rp.Evidence) > 0 {
			if b, err := json.Marshal(rp.Evidence); err == nil {
				evidenceJSON = string(b)
			}
		}

		conf := rp.Confidence
		if conf < 0 {
			conf = 0
		}
		if conf > 1 {
			conf = 1
		}

		signals := rp.SignalCount
		if signals < 1 {
			signals = 1
		}

		patterns = append(patterns, models.TrendPattern{
			PatternType: models.TrendPatternType(rp.PatternType),
			Title:       rp.Title,
			Description: rp.Description,
			Evidence:    evidenceJSON,
			Confidence:  conf,
			SignalCount: signals,
			Status:      models.PatternStatusActive,
		})
	}

	return patterns, nil
}

func (s *TrendIntelligenceService) parseCompetitorUpdatesResponse(response string) ([]models.CompetitorUpdate, error) {
	arrayStr := util.ExtractJSONArray(response)
	if arrayStr == "" {
		return nil, fmt.Errorf("no JSON array found in response")
	}

	var rawUpdates []struct {
		CompetitorName   string  `json:"competitor_name"`
		UpdateType       string  `json:"update_type"`
		Title            string  `json:"title"`
		Description      string  `json:"description"`
		ImpactAssessment string  `json:"impact_assessment"`
		RelevanceScore   float64 `json:"relevance_score"`
	}

	if err := json.Unmarshal([]byte(arrayStr), &rawUpdates); err != nil {
		return nil, fmt.Errorf("parse competitor JSON: %w", err)
	}

	var updates []models.CompetitorUpdate
	for _, ru := range rawUpdates {
		score := ru.RelevanceScore
		if score < 0 {
			score = 0
		}
		if score > 1 {
			score = 1
		}

		updates = append(updates, models.CompetitorUpdate{
			CompetitorName:   ru.CompetitorName,
			UpdateType:       models.CompetitorUpdateType(ru.UpdateType),
			Title:            ru.Title,
			Description:      ru.Description,
			ImpactAssessment: ru.ImpactAssessment,
			RelevanceScore:   score,
		})
	}

	return updates, nil
}

